//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

const (
	mmService       = "org.freedesktop.ModemManager1"
	mmRootPath      = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	mmManager       = "org.freedesktop.ModemManager1"
	mmModem         = "org.freedesktop.ModemManager1.Modem"
	mmModemSimple   = mmModem + ".Simple"
	mmModem3GPP     = "org.freedesktop.ModemManager1.Modem.Modem3gpp"
	mmSIM           = "org.freedesktop.ModemManager1.Sim"
	mmBearer        = "org.freedesktop.ModemManager1.Bearer"
	objectManager   = "org.freedesktop.DBus.ObjectManager.GetManagedObjects"
	mmInhibitDevice = mmManager + ".InhibitDevice"
	dbusProperties  = "org.freedesktop.DBus.Properties.GetAll"

	mmPortNet   = uint32(2)
	mmPortAT    = uint32(3)
	mmPortAudio = uint32(8)
)

type managedObjects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

type modemSnapshot struct {
	ObjectPath    dbus.ObjectPath
	UID           string
	EquipmentID   string
	Manufacturer  string
	Model         string
	Firmware      string
	ATPorts       []string
	NetPorts      []string
	AudioPorts    []string
	Bearers       []dbus.ObjectPath
	Connected     bool
	SIMState      agentmodem.SIMState
	ICCID         string
	IMSI          string
	MSISDNs       []string
	OperatorID    string
	OperatorName  string
	Registration  agentmodem.RegistrationState
	SignalPercent *uint32
}

type modemManager interface {
	Inventory(context.Context) ([]modemSnapshot, error)
	Inhibit(context.Context, string, bool) error
	Connect(context.Context, dbus.ObjectPath, agentdata.Profile) (dataBearer, error)
	Disconnect(context.Context, dbus.ObjectPath) error
	Close() error
}

type dataBearer struct {
	ObjectPath dbus.ObjectPath
	Interface  string
	Address    string
	Prefix     uint32
	Gateway    string
	DNS        []string
}

type dbusModemManager struct {
	connection *dbus.Conn
	root       dbus.BusObject
}

func openModemManager() (modemManager, error) {
	connection, err := dbus.SystemBusPrivate()
	if err != nil {
		return nil, fmt.Errorf("open private system D-Bus: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = connection.Close()
		}
	}()
	if err := connection.Auth(nil); err != nil {
		return nil, fmt.Errorf("authenticate system D-Bus: %w", err)
	}
	if err := connection.Hello(); err != nil {
		return nil, fmt.Errorf("join system D-Bus: %w", err)
	}
	failed = false
	return &dbusModemManager{
		connection: connection,
		root:       connection.Object(mmService, mmRootPath),
	}, nil
}

func (manager *dbusModemManager) Inventory(ctx context.Context) ([]modemSnapshot, error) {
	var objects managedObjects
	if err := manager.root.CallWithContext(ctx, objectManager, 0).Store(&objects); err != nil {
		return nil, fmt.Errorf("inventory ModemManager objects: %w", err)
	}
	return parseManagedObjects(objects)
}

func (manager *dbusModemManager) Inhibit(ctx context.Context, uid string, inhibit bool) error {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return errors.New("ModemManager device UID is empty")
	}
	if err := manager.root.CallWithContext(ctx, mmInhibitDevice, 0, uid, inhibit).Err; err != nil {
		action := "inhibit"
		if !inhibit {
			action = "uninhibit"
		}
		return fmt.Errorf("%s ModemManager device: %w", action, err)
	}
	return nil
}

func (manager *dbusModemManager) Connect(ctx context.Context, modemPath dbus.ObjectPath, profile agentdata.Profile) (dataBearer, error) {
	if !modemPath.IsValid() || modemPath == "/" || len(profile.APN) > 256 || strings.ContainsAny(profile.APN, "\r\n\x00") {
		return dataBearer{}, errors.New("invalid ModemManager data connection target")
	}
	settings := map[string]dbus.Variant{
		"allow-roaming": dbus.MakeVariant(profile.AllowRoaming),
		"ip-type":       dbus.MakeVariant(uint32(1)),
	}
	if apn := strings.TrimSpace(profile.APN); apn != "" {
		settings["apn"] = dbus.MakeVariant(apn)
	}
	if profile.Username != "" {
		settings["user"] = dbus.MakeVariant(profile.Username)
	}
	if profile.Password != "" {
		settings["password"] = dbus.MakeVariant(profile.Password)
	}
	switch profile.Auth {
	case "PAP":
		settings["allowed-auth"] = dbus.MakeVariant(uint32(2))
	case "CHAP":
		settings["allowed-auth"] = dbus.MakeVariant(uint32(4))
	case "MSCHAPV2":
		settings["allowed-auth"] = dbus.MakeVariant(uint32(16))
	}
	var bearerPath dbus.ObjectPath
	object := manager.connection.Object(mmService, modemPath)
	if err := object.CallWithContext(ctx, mmModemSimple+".Connect", 0, settings).Store(&bearerPath); err != nil {
		return dataBearer{}, fmt.Errorf("connect ModemManager bearer: %w", err)
	}
	bearer, err := manager.readBearer(ctx, bearerPath)
	if err != nil {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = manager.Disconnect(cleanupContext, bearerPath)
		cancel()
		return dataBearer{}, err
	}
	return bearer, nil
}

func (manager *dbusModemManager) readBearer(ctx context.Context, path dbus.ObjectPath) (dataBearer, error) {
	if !path.IsValid() || path == "/" {
		return dataBearer{}, errors.New("ModemManager returned an invalid bearer path")
	}
	var properties map[string]dbus.Variant
	if err := manager.connection.Object(mmService, path).CallWithContext(ctx, dbusProperties, 0, mmBearer).Store(&properties); err != nil {
		return dataBearer{}, fmt.Errorf("read ModemManager bearer: %w", err)
	}
	return parseDataBearer(path, properties)
}

func parseDataBearer(path dbus.ObjectPath, properties map[string]dbus.Variant) (dataBearer, error) {
	result := dataBearer{ObjectPath: path, Interface: stringProperty(properties, "Interface")}
	connected, _ := variantValue[bool](properties, "Connected")
	config, _ := variantValue[map[string]dbus.Variant](properties, "Ip4Config")
	method := uint32Property(config, "method")
	result.Address = stringProperty(config, "address")
	result.Prefix = uint32Property(config, "prefix")
	result.Gateway = stringProperty(config, "gateway")
	result.DNS = append(result.DNS, stringSliceProperty(config, "dns")...)
	for _, key := range []string{"dns1", "dns2", "dns3"} {
		if value := stringProperty(config, key); value != "" {
			result.DNS = append(result.DNS, value)
		}
	}
	result.DNS = boundedStrings(result.DNS, 3, 64)
	if !connected || result.Interface == "" || filepath.Base(result.Interface) != result.Interface || method != 2 ||
		result.Address == "" || result.Prefix == 0 || result.Prefix > 32 {
		return dataBearer{}, errors.New("ModemManager bearer is not connected with usable static IPv4 configuration")
	}
	return result, nil
}

func (manager *dbusModemManager) Disconnect(ctx context.Context, path dbus.ObjectPath) error {
	if !path.IsValid() || path == "/" {
		return nil
	}
	if err := manager.connection.Object(mmService, path).CallWithContext(ctx, mmBearer+".Disconnect", 0).Err; err != nil {
		return fmt.Errorf("disconnect ModemManager bearer: %w", err)
	}
	return nil
}

func variantValue[T any](properties map[string]dbus.Variant, name string) (T, bool) {
	var zero T
	value, exists := properties[name]
	if !exists {
		return zero, false
	}
	result, ok := value.Value().(T)
	return result, ok
}

func (manager *dbusModemManager) Close() error {
	if manager.connection == nil {
		return nil
	}
	connection := manager.connection
	manager.connection = nil
	return connection.Close()
}

func parseManagedObjects(objects managedObjects) ([]modemSnapshot, error) {
	result := make([]modemSnapshot, 0)
	for path, interfaces := range objects {
		properties, exists := interfaces[mmModem]
		if !exists {
			continue
		}
		value := modemSnapshot{
			ObjectPath:   path,
			UID:          stringProperty(properties, "Device"),
			EquipmentID:  digitsOnly(stringProperty(properties, "EquipmentIdentifier"), 14, 17),
			Manufacturer: bounded(stringProperty(properties, "Manufacturer"), 256),
			Model:        bounded(stringProperty(properties, "Model"), 256),
			Firmware:     bounded(stringProperty(properties, "Revision"), 256),
			MSISDNs:      boundedStrings(stringSliceProperty(properties, "OwnNumbers"), 16, 64),
			SIMState:     agentmodem.SIMUnknown,
			Registration: agentmodem.RegistrationUnknown,
		}
		if value.UID == "" || value.EquipmentID == "" {
			continue
		}
		ports, err := portProperty(properties, "Ports")
		if err != nil {
			return nil, fmt.Errorf("decode ModemManager ports for %s: %w", path, err)
		}
		for _, port := range ports {
			switch port.kind {
			case mmPortNet:
				value.NetPorts = append(value.NetPorts, port.name)
			case mmPortAT:
				value.ATPorts = append(value.ATPorts, port.name)
			case mmPortAudio:
				value.AudioPorts = append(value.AudioPorts, port.name)
			}
		}
		sort.Strings(value.ATPorts)
		sort.Strings(value.NetPorts)
		sort.Strings(value.AudioPorts)
		value.Connected = modemConnected(properties, objects)
		for _, bearerPath := range objectPathSliceProperty(properties, "Bearers") {
			if interfaces, ok := objects[bearerPath]; ok {
				if bearer, ok := interfaces[mmBearer]; ok && boolProperty(bearer, "Connected") {
					value.Bearers = append(value.Bearers, bearerPath)
				}
			}
		}
		if quality, recent, ok := signalProperty(properties, "SignalQuality"); ok && recent && quality <= 100 {
			value.SignalPercent = &quality
		}
		if properties3GPP, ok := interfaces[mmModem3GPP]; ok {
			value.Registration = registrationState(uint32Property(properties3GPP, "RegistrationState"))
			value.OperatorID = digitsOnly(stringProperty(properties3GPP, "OperatorCode"), 5, 6)
			value.OperatorName = bounded(stringProperty(properties3GPP, "OperatorName"), 256)
		}
		simPath := objectPathProperty(properties, "Sim")
		if simPath != "" && simPath != "/" {
			if simInterfaces, ok := objects[simPath]; ok {
				if sim, ok := simInterfaces[mmSIM]; ok {
					value.ICCID = digitsOnly(stringProperty(sim, "SimIdentifier"), 18, 22)
					value.IMSI = digitsOnly(stringProperty(sim, "Imsi"), 5, 16)
					if value.OperatorID == "" {
						value.OperatorID = digitsOnly(stringProperty(sim, "OperatorIdentifier"), 5, 6)
					}
					if value.OperatorName == "" {
						value.OperatorName = bounded(stringProperty(sim, "OperatorName"), 256)
					}
				}
			}
		}
		unlockRequired := uint32Property(properties, "UnlockRequired")
		switch {
		case value.ICCID != "" && unlockRequired == 1:
			value.SIMState = agentmodem.SIMReady
		case unlockRequired > 1:
			value.SIMState = agentmodem.SIMLocked
		case simPath == "" || simPath == "/":
			value.SIMState = agentmodem.SIMAbsent
		}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UID == result[right].UID {
			return result[left].EquipmentID < result[right].EquipmentID
		}
		return result[left].UID < result[right].UID
	})
	for index := 1; index < len(result); index++ {
		if result[index-1].UID == result[index].UID {
			return nil, errors.New("ModemManager exported duplicate physical device UIDs")
		}
	}
	return result, nil
}

type modemPort struct {
	name string
	kind uint32
}

func portProperty(properties map[string]dbus.Variant, name string) ([]modemPort, error) {
	variant, ok := properties[name]
	if !ok {
		return nil, nil
	}
	value := reflect.ValueOf(variant.Value())
	if value.Kind() != reflect.Slice {
		return nil, errors.New("ports property is not an array")
	}
	result := make([]modemPort, 0, value.Len())
	for index := 0; index < value.Len(); index++ {
		entry := value.Index(index)
		if entry.Kind() == reflect.Interface {
			entry = entry.Elem()
		}
		var nameValue, kindValue reflect.Value
		switch entry.Kind() {
		case reflect.Struct:
			if entry.NumField() != 2 {
				return nil, errors.New("port tuple has the wrong width")
			}
			nameValue, kindValue = entry.Field(0), entry.Field(1)
		case reflect.Slice, reflect.Array:
			if entry.Len() != 2 {
				return nil, errors.New("port tuple has the wrong width")
			}
			nameValue, kindValue = entry.Index(0), entry.Index(1)
		default:
			return nil, errors.New("port entry is not a tuple")
		}
		for nameValue.Kind() == reflect.Interface {
			nameValue = nameValue.Elem()
		}
		for kindValue.Kind() == reflect.Interface {
			kindValue = kindValue.Elem()
		}
		kind, ok := reflectUint32(kindValue)
		if nameValue.Kind() != reflect.String || !ok {
			return nil, errors.New("port tuple has invalid field types")
		}
		portName := strings.TrimSpace(nameValue.String())
		if portName == "" || filepath.Base(portName) != portName {
			return nil, errors.New("port tuple has an invalid device name")
		}
		result = append(result, modemPort{name: portName, kind: kind})
	}
	return result, nil
}

func modemConnected(properties map[string]dbus.Variant, objects managedObjects) bool {
	// CONNECTING, CONNECTED and DISCONNECTING are all unsafe ownership handoff
	// points. Inhibit must never be used as a surprise data-session terminator.
	state := int32Property(properties, "State")
	if state >= 9 {
		return true
	}
	for _, path := range objectPathSliceProperty(properties, "Bearers") {
		if interfaces, ok := objects[path]; ok {
			if bearer, ok := interfaces[mmBearer]; ok && boolProperty(bearer, "Connected") {
				return true
			}
		}
	}
	return false
}

func registrationState(value uint32) agentmodem.RegistrationState {
	switch value {
	case 1:
		return agentmodem.RegistrationHome
	case 2:
		return agentmodem.RegistrationSearching
	case 3:
		return agentmodem.RegistrationDenied
	case 5:
		return agentmodem.RegistrationRoaming
	default:
		return agentmodem.RegistrationUnknown
	}
}

func stringProperty(properties map[string]dbus.Variant, name string) string {
	if value, ok := properties[name]; ok {
		text, _ := value.Value().(string)
		return strings.TrimSpace(text)
	}
	return ""
}

func uint32Property(properties map[string]dbus.Variant, name string) uint32 {
	if value, ok := properties[name]; ok {
		number, _ := value.Value().(uint32)
		return number
	}
	return 0
}

func int32Property(properties map[string]dbus.Variant, name string) int32 {
	if value, ok := properties[name]; ok {
		number, _ := value.Value().(int32)
		return number
	}
	return 0
}

func boolProperty(properties map[string]dbus.Variant, name string) bool {
	if value, ok := properties[name]; ok {
		result, _ := value.Value().(bool)
		return result
	}
	return false
}

func stringSliceProperty(properties map[string]dbus.Variant, name string) []string {
	if value, ok := properties[name]; ok {
		result, _ := value.Value().([]string)
		return append([]string(nil), result...)
	}
	return nil
}

func objectPathProperty(properties map[string]dbus.Variant, name string) dbus.ObjectPath {
	if value, ok := properties[name]; ok {
		result, _ := value.Value().(dbus.ObjectPath)
		return result
	}
	return ""
}

func objectPathSliceProperty(properties map[string]dbus.Variant, name string) []dbus.ObjectPath {
	if value, ok := properties[name]; ok {
		result, _ := value.Value().([]dbus.ObjectPath)
		return append([]dbus.ObjectPath(nil), result...)
	}
	return nil
}

func signalProperty(properties map[string]dbus.Variant, name string) (uint32, bool, bool) {
	variant, ok := properties[name]
	if !ok {
		return 0, false, false
	}
	value := reflect.ValueOf(variant.Value())
	if value.Kind() == reflect.Interface {
		value = value.Elem()
	}
	var first, second reflect.Value
	switch value.Kind() {
	case reflect.Struct:
		if value.NumField() != 2 {
			return 0, false, false
		}
		first, second = value.Field(0), value.Field(1)
	case reflect.Slice, reflect.Array:
		if value.Len() != 2 {
			return 0, false, false
		}
		first, second = value.Index(0), value.Index(1)
	default:
		return 0, false, false
	}
	for first.Kind() == reflect.Interface {
		first = first.Elem()
	}
	for second.Kind() == reflect.Interface {
		second = second.Elem()
	}
	quality, ok := reflectUint32(first)
	if !ok || second.Kind() != reflect.Bool {
		return 0, false, false
	}
	return quality, second.Bool(), true
}

func reflectUint32(value reflect.Value) (uint32, bool) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		value = value.Elem()
	}
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value.Uint() <= uint64(^uint32(0)) {
			return uint32(value.Uint()), true
		}
	}
	return 0, false
}

func digitsOnly(value string, minimum, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) < minimum || len(value) > maximum {
		return ""
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return ""
		}
	}
	return value
}

func boundedStrings(values []string, maximum, width int) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, min(len(values), maximum))
	for _, value := range values {
		value = bounded(value, width)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == maximum {
			break
		}
	}
	return result
}
