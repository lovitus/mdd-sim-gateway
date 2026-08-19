using System.Text.Json;
using System.Runtime.InteropServices;
using Windows.Win32;
using Windows.Win32.Foundation;
using Windows.Win32.NetworkManagement.MobileBroadband;
using Windows.Win32.System.Com;

namespace Mdd.WindowsMbn;

internal static unsafe class Program
{
    // The value is not cached yet; MBN publishes it through the change notification.
    private const int E_PENDING = unchecked((int)0x8000000A);

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        WriteIndented = false,
    };

    public static int Main(string[] args)
    {
        try
        {
            if (args.Length == 1 && args[0] == "probe")
            {
                var manager = (IMbnInterfaceManager)new MbnInterfaceManager();
                manager.GetInterfaces(out var array);
                var values = ReadInterfaceArray(array).Select(Probe).ToArray();
                return Write(new { ok = true, interfaces = values }, 0);
            }
            if (args.Length == 3 && args[0] == "connect")
            {
                return Connect(args[1], args[2]);
            }
            if (args.Length == 2 && args[0] == "disconnect")
            {
                return Disconnect(args[1]);
            }
            if (args.Length == 2 && args[0] == "sms-config")
            {
                return SmsConfig(args[1]);
            }
            if (args.Length == 2 && args[0] == "sms-read")
            {
                return SmsRead(args[1]);
            }
            if (args.Length == 2 && args[0] == "sms-send")
            {
                using var request = JsonDocument.Parse(Console.In.ReadToEnd());
                return SmsSend(args[1], request.RootElement.GetProperty("pdu").GetString() ?? "",
                    request.RootElement.GetProperty("size").GetByte());
            }
            if (args.Length == 2 && args[0] == "sms-delete")
            {
                using var request = JsonDocument.Parse(Console.In.ReadToEnd());
                return SmsDelete(args[1], request.RootElement.GetProperty("index").GetUInt32());
            }
            return Write(new
            {
                ok = false,
                error = "usage: mdd-windows-mbn probe | connect <interface-id> <profile> | disconnect <interface-id>",
            }, 2);
        }
        catch (Exception exception)
        {
            return Write(new
            {
                ok = false,
                error = exception.Message,
                error_type = exception.GetType().Name,
                hresult = $"0x{exception.HResult:X8}",
            }, 1);
        }
    }

    /// <summary>
    /// Read the SMS configuration, waiting once for the driver to publish it.
    /// </summary>
    /// <remarks>
    /// Observed on real hardware (EC20, 2026-08-19): <c>GetSmsConfiguration</c> answers
    /// E_PENDING while <c>netsh mbn show smsconfig</c> displays a valid service centre.
    /// E_PENDING means the value is not cached yet and will arrive through
    /// <c>OnSmsConfigurationChange</c>, so a single read must not be reported as "no
    /// configuration". This lives in its own verb because the wait is bounded but not free:
    /// the <c>probe</c> verb backs every status heartbeat and must stay non-blocking.
    /// </remarks>
    private static int SmsConfig(string interfaceId)
    {
        var manager = (IMbnInterfaceManager)new MbnInterfaceManager();
        manager.GetInterface(interfaceId, out var value);
        var sms = (IMbnSms)value;
        var sink = new SmsEvents();
        using var subscription = ConnectionPointSubscription.Create<IMbnSmsEvents>(manager, sink);
        for (var attempt = 0; attempt < 2; attempt++)
        {
            try
            {
                sms.GetSmsConfiguration(out var configuration);
                BSTR serviceCenter = default;
                configuration.get_ServiceCenterAddress(&serviceCenter);
                return Write(new
                {
                    ok = true,
                    service_center = Take(serviceCenter),
                    sms_format = configuration.SmsFormat.ToString(),
                    max_message_index = configuration.MaxMessageIndex,
                    attempts = attempt + 1,
                }, 0);
            }
            catch (COMException exception) when (exception.HResult == E_PENDING && attempt == 0)
            {
                if (!sink.ConfigurationChanged.Wait(TimeSpan.FromSeconds(10)))
                {
                    return Write(new
                    {
                        ok = false,
                        pending = true,
                        service_center = "",
                        error = "Windows MBN did not publish the SMS configuration within 10 seconds",
                        hresult = $"0x{exception.HResult:X8}",
                    }, 1);
                }
            }
            catch (COMException exception)
            {
                return Write(new
                {
                    ok = false, service_center = "", error = exception.Message,
                    hresult = $"0x{exception.HResult:X8}",
                }, 1);
            }
        }
        return Write(new { ok = false, service_center = "", error = "unreachable" }, 1);
    }

    private static int SmsRead(string interfaceId)
    {
        var manager = (IMbnInterfaceManager)new MbnInterfaceManager();
        manager.GetInterface(interfaceId, out var value);
        var sms = (IMbnSms)value;
        var sink = new SmsEvents();
        using var subscription = ConnectionPointSubscription.Create<IMbnSmsEvents>(manager, sink);
        var filter = new MBN_SMS_FILTER { flag = MBN_SMS_FLAG.MBN_SMS_FLAG_ALL };
        sms.SmsRead(&filter, MBN_SMS_FORMAT.MBN_SMS_FORMAT_PDU, out var requestId);
        sink.RequestId = requestId;
        if (!sink.Completed.Wait(TimeSpan.FromSeconds(30)))
        {
            return Write(new { ok = false, status = "unknown", request_id = requestId,
                error = "Windows MBN did not complete the SMS read request within 45 seconds" }, 1);
        }
        return Write(new { ok = sink.Status >= 0,
            status = sink.Status >= 0 ? "completed" : "failed", request_id = requestId,
            hresult = $"0x{sink.Status:X8}", messages = sink.Messages }, sink.Status >= 0 ? 0 : 1);
    }

    private static int SmsSend(string interfaceId, string pdu, byte size)
    {
        if (string.IsNullOrWhiteSpace(pdu) || pdu.Length % 2 != 0 ||
            pdu.Any(value => !Uri.IsHexDigit(value)) || size == 0)
        {
            return Write(new { ok = false, status = "invalid", error = "invalid SMS PDU" }, 2);
        }
        var manager = (IMbnInterfaceManager)new MbnInterfaceManager();
        manager.GetInterface(interfaceId, out var value);
        var sms = (IMbnSms)value;
        var sink = new SmsEvents();
        using var subscription = ConnectionPointSubscription.Create<IMbnSmsEvents>(manager, sink);
        sms.SmsSendPdu(pdu, size, out var requestId);
        sink.RequestId = requestId;
        if (!sink.Completed.Wait(TimeSpan.FromSeconds(180)))
        {
            return Write(new { ok = false, status = "unknown", request_id = requestId,
                error = "Windows MBN did not complete the SMS send request within 180 seconds" }, 1);
        }
        return Write(new { ok = sink.Status >= 0,
            status = sink.Status >= 0 ? "sent" : "failed", request_id = requestId,
            hresult = $"0x{sink.Status:X8}" }, sink.Status >= 0 ? 0 : 1);
    }

    private static int SmsDelete(string interfaceId, uint index)
    {
        if (index == 0)
        {
            return Write(new { ok = false, status = "invalid", error = "invalid SMS index" }, 2);
        }
        var manager = (IMbnInterfaceManager)new MbnInterfaceManager();
        manager.GetInterface(interfaceId, out var value);
        var sms = (IMbnSms)value;
        var sink = new SmsEvents();
        using var subscription = ConnectionPointSubscription.Create<IMbnSmsEvents>(manager, sink);
        var filter = new MBN_SMS_FILTER
        {
            flag = MBN_SMS_FLAG.MBN_SMS_FLAG_INDEX,
            messageIndex = index,
        };
        sms.SmsDelete(&filter, out var requestId);
        sink.RequestId = requestId;
        if (!sink.Completed.Wait(TimeSpan.FromSeconds(45)))
        {
            return Write(new { ok = false, status = "unknown", request_id = requestId,
                error = "Windows MBN did not complete the SMS delete request within 45 seconds" }, 1);
        }
        return Write(new { ok = sink.Status >= 0,
            status = sink.Status >= 0 ? "deleted" : "failed", request_id = requestId,
            hresult = $"0x{sink.Status:X8}" }, sink.Status >= 0 ? 0 : 1);
    }

    private static int Connect(string interfaceId, string profile)
    {
        if (string.IsNullOrWhiteSpace(profile))
        {
            return Write(new { ok = false, status = "invalid", error = "profile is required" }, 2);
        }
        var connection = FindInterface(interfaceId).GetConnection();
        var eventManager = (IMbnConnectionManager)new MbnConnectionManager();
        var sink = new ConnectionEvents();
        using var subscription = ConnectionPointSubscription.Create<IMbnConnectionEvents>(eventManager, sink);
        connection.Connect(MBN_CONNECTION_MODE.MBN_CONNECTION_MODE_PROFILE, profile, out var requestId);
        sink.RequestId = requestId;
        if (!sink.Completed.Wait(TimeSpan.FromSeconds(45)))
        {
            return Write(new { ok = false, status = "unknown", request_id = requestId,
                error = "Windows MBN did not complete the connection request within 30 seconds" }, 1);
        }
        var state = ReadConnectionState(connection);
        if (sink.Status >= 0)
        {
            // The completion callback acknowledges the asynchronous request; several WWAN
            // miniports publish ACTIVATED slightly later.  Treat the observable target state,
            // not the callback instant, as the operation postcondition.
            var deadline = DateTime.UtcNow.AddSeconds(5);
            while (state.State != MBN_ACTIVATION_STATE.MBN_ACTIVATION_STATE_ACTIVATED &&
                   DateTime.UtcNow < deadline)
            {
                Thread.Sleep(500);
                state = ReadConnectionState(connection);
            }
        }
        connection.GetActivationNetworkError(out var networkError);
        return Write(new
        {
            ok = sink.Status >= 0 && state.State == MBN_ACTIVATION_STATE.MBN_ACTIVATION_STATE_ACTIVATED,
            status = sink.Status >= 0 ? "completed" : "failed",
            request_id = requestId,
            hresult = $"0x{sink.Status:X8}",
            network_error = networkError,
            activation_state = state.State.ToString(),
            profile_name = state.Profile,
        }, sink.Status >= 0 ? 0 : 1);
    }

    private static int Disconnect(string interfaceId)
    {
        var connection = FindInterface(interfaceId).GetConnection();
        var eventManager = (IMbnConnectionManager)new MbnConnectionManager();
        var sink = new ConnectionEvents();
        using var subscription = ConnectionPointSubscription.Create<IMbnConnectionEvents>(eventManager, sink);
        connection.Disconnect(out var requestId);
        sink.RequestId = requestId;
        if (!sink.Completed.Wait(TimeSpan.FromSeconds(30)))
        {
            return Write(new { ok = false, status = "unknown", request_id = requestId,
                error = "Windows MBN did not complete the disconnect request within 30 seconds" }, 1);
        }
        var state = ReadConnectionState(connection);
        if (sink.Status >= 0)
        {
            var deadline = DateTime.UtcNow.AddSeconds(20);
            while (state.State == MBN_ACTIVATION_STATE.MBN_ACTIVATION_STATE_ACTIVATED &&
                   DateTime.UtcNow < deadline)
            {
                Thread.Sleep(500);
                state = ReadConnectionState(connection);
            }
        }
        return Write(new
        {
            ok = sink.Status >= 0 && state.State != MBN_ACTIVATION_STATE.MBN_ACTIVATION_STATE_ACTIVATED,
            status = sink.Status >= 0 ? "completed" : "failed",
            request_id = requestId,
            hresult = $"0x{sink.Status:X8}",
            activation_state = state.State.ToString(),
        }, sink.Status >= 0 ? 0 : 1);
    }

    private static IMbnInterface FindInterface(string interfaceId)
    {
        var manager = (IMbnInterfaceManager)new MbnInterfaceManager();
        manager.GetInterface(interfaceId, out var value);
        return value;
    }

    private static (MBN_ACTIVATION_STATE State, string Profile) ReadConnectionState(
        IMbnConnection connection)
    {
        var state = MBN_ACTIVATION_STATE.MBN_ACTIVATION_STATE_NONE;
        BSTR profile = default;
        connection.GetConnectionState(&state, &profile);
        return (state, Take(profile));
    }

    private static object Probe(IMbnInterface value)
    {
        value.GetInterfaceCapability(out var caps);
        var subscriber = value.GetSubscriberInformation();
        var connection = value.GetConnection();
        var connectionState = ReadConnectionState(connection);
        value.GetReadyState(out var readyState);
        var network = ReadNetwork(value);
        var sms = ReadSms(value);
        var result = new
        {
            interface_id = Take(value.InterfaceID),
            device_id = Take(caps.deviceID),
            manufacturer = Take(caps.manufacturer),
            model = Take(caps.model),
            firmware = Take(caps.firmwareInfo),
            cellular_class = caps.cellularClass.ToString(),
            voice_class = caps.voiceClass.ToString(),
            sms_caps = caps.smsCaps,
            sms_ready = sms.Ready,
            sms_configured = sms.Configured,
            sms_service_center = sms.ServiceCenter,
            sms_format = sms.Format,
            sms_error = sms.Error,
            data_class = caps.dataClass,
            ready_state = readyState.ToString(),
            subscriber_id = Take(subscriber.SubscriberID),
            sim_iccid = Take(subscriber.SimIccID),
            telephone_numbers = ReadBstrArray(subscriber.TelephoneNumbers),
            activation_state = connectionState.State.ToString(),
            profile_name = connectionState.Profile,
            registration = network.Registration,
            provider_id = network.ProviderId,
            provider_name = network.ProviderName,
            signal = network.Signal,
            software_radio = network.SoftwareRadio,
            hardware_radio = network.HardwareRadio,
        };
        Take(caps.customDataClass);
        Take(caps.customBandClass);
        return result;
    }

    private static (bool? Ready, bool Configured, string ServiceCenter, string Format, string Error)
        ReadSms(IMbnInterface value)
    {
        try
        {
            var sms = (IMbnSms)value;
            sms.GetSmsConfiguration(out var configuration);
            BSTR serviceCenter = default;
            configuration.get_ServiceCenterAddress(&serviceCenter);
            var status = new MBN_SMS_STATUS_INFO();
            sms.GetSmsStatus(&status);
            // GetSmsConfiguration and GetSmsStatus prove that Windows can read the
            // configuration and message store. Neither API exposes the "Ready to send SMS"
            // state shown by netsh, so do not turn successful reads into a false-positive
            // network readiness signal. The Agent resolves runtime readiness independently.
            return (null, true, Take(serviceCenter), configuration.SmsFormat.ToString(), "");
        }
        catch (COMException exception)
        {
            return (false, false, "", "", $"0x{exception.HResult:X8}");
        }
    }

    private static (string Registration, string ProviderId, string ProviderName,
        uint? Signal, string SoftwareRadio, string HardwareRadio) ReadNetwork(IMbnInterface value)
    {
        var registrationName = "MBN_REGISTER_STATE_UNKNOWN";
        var providerId = "";
        var providerName = "";
        uint? signal = null;
        var softwareRadio = "unknown";
        var hardwareRadio = "unknown";
        try
        {
            var registration = (IMbnRegistration)value;
            var state = MBN_REGISTER_STATE.MBN_REGISTER_STATE_NONE;
            registration.GetRegisterState(&state);
            registrationName = state.ToString();
            BSTR id = default;
            BSTR name = default;
            registration.GetProviderID(&id);
            registration.GetProviderName(&name);
            providerId = Take(id);
            providerName = Take(name);
        }
        catch (COMException) { }
        try
        {
            ((IMbnSignal)value).GetSignalStrength(out var strength);
            if (strength <= 100) signal = strength;
        }
        catch (COMException) { }
        try
        {
            var radio = (IMbnRadio)value;
            softwareRadio = radio.SoftwareRadioState.ToString();
            hardwareRadio = radio.HardwareRadioState.ToString();
        }
        catch (COMException) { }
        return (registrationName, providerId, providerName, signal, softwareRadio, hardwareRadio);
    }

    private static IReadOnlyList<IMbnInterface> ReadInterfaceArray(SAFEARRAY* array)
    {
        var result = new List<IMbnInterface>();
        if (array is null)
        {
            return result;
        }
        try
        {
            NativeArray.Bounds(array, out var lower, out var upper);
            for (var index = lower; index <= upper; index++)
            {
                NativeArray.Element(array, index, out var unknown);
                if (unknown == IntPtr.Zero)
                {
                    continue;
                }
                try
                {
                    result.Add((IMbnInterface)Marshal.GetObjectForIUnknown(unknown));
                }
                finally
                {
                    Marshal.Release(unknown);
                }
            }
            return result;
        }
        finally
        {
            NativeArray.Destroy(array);
        }
    }

    private static string[] ReadBstrArray(SAFEARRAY* array)
    {
        var result = new List<string>();
        if (array is null)
        {
            return result.ToArray();
        }
        try
        {
            NativeArray.Bounds(array, out var lower, out var upper);
            for (var index = lower; index <= upper; index++)
            {
                NativeArray.Element(array, index, out var raw);
                if (raw != IntPtr.Zero)
                {
                    result.Add(Marshal.PtrToStringBSTR(raw) ?? "");
                    Marshal.FreeBSTR(raw);
                }
            }
            return result.ToArray();
        }
        finally
        {
            NativeArray.Destroy(array);
        }
    }

    internal static string Take(BSTR value)
    {
        var result = value.ToString() ?? "";
        if (value.Value is not null)
        {
            Marshal.FreeBSTR((IntPtr)value.Value);
        }
        return result;
    }

    private static int Write(object value, int exitCode)
    {
        Console.WriteLine(JsonSerializer.Serialize(value, JsonOptions));
        return exitCode;
    }
}

internal static unsafe class NativeArray
{
    [DllImport("oleaut32.dll", PreserveSig = false)]
    private static extern void SafeArrayGetLBound(SAFEARRAY* array, uint dimension, out int value);

    [DllImport("oleaut32.dll", PreserveSig = false)]
    private static extern void SafeArrayGetUBound(SAFEARRAY* array, uint dimension, out int value);

    [DllImport("oleaut32.dll", PreserveSig = false)]
    private static extern void SafeArrayGetElement(SAFEARRAY* array, ref int indices, out IntPtr value);

    [DllImport("oleaut32.dll", PreserveSig = false)]
    private static extern void SafeArrayDestroy(SAFEARRAY* array);

    internal static void Bounds(SAFEARRAY* array, out int lower, out int upper)
    {
        SafeArrayGetLBound(array, 1, out lower);
        SafeArrayGetUBound(array, 1, out upper);
    }

    internal static void Element(SAFEARRAY* array, int index, out IntPtr value) =>
        SafeArrayGetElement(array, ref index, out value);

    internal static void Destroy(SAFEARRAY* array) => SafeArrayDestroy(array);

    internal static IReadOnlyList<T> ReadUnknown<T>(SAFEARRAY* array, bool destroy)
    {
        var result = new List<T>();
        if (array is null) return result;
        try
        {
            Bounds(array, out var lower, out var upper);
            for (var index = lower; index <= upper; index++)
            {
                Element(array, index, out var unknown);
                if (unknown == IntPtr.Zero) continue;
                try
                {
                    result.Add((T)Marshal.GetObjectForIUnknown(unknown));
                }
                finally
                {
                    Marshal.Release(unknown);
                }
            }
            return result;
        }
        finally
        {
            if (destroy) Destroy(array);
        }
    }
}

[ComVisible(true)]
[ClassInterface(ClassInterfaceType.None)]
internal sealed class ConnectionEvents : IMbnConnectionEvents
{
    internal readonly ManualResetEventSlim Completed = new(false);
    internal uint RequestId { get; set; }
    internal int Status { get; private set; }

    public void OnConnectComplete(IMbnConnection connection, uint requestID, HRESULT status)
    {
        if (RequestId != 0 && requestID != RequestId) return;
        RequestId = requestID;
        Status = status.Value;
        Completed.Set();
    }

    public void OnDisconnectComplete(IMbnConnection connection, uint requestID, HRESULT status)
    {
        if (RequestId != 0 && requestID != RequestId) return;
        RequestId = requestID;
        Status = status.Value;
        Completed.Set();
    }

    public void OnConnectStateChange(IMbnConnection connection) { }

    public void OnVoiceCallStateChange(IMbnConnection connection) { }
}

[ComVisible(true)]
[ClassInterface(ClassInterfaceType.None)]
internal sealed unsafe class SmsEvents : IMbnSmsEvents
{
    internal readonly ManualResetEventSlim Completed = new(false);
    // Signalled when the driver publishes an SMS configuration that was previously E_PENDING.
    internal readonly ManualResetEventSlim ConfigurationChanged = new(false);
    internal uint RequestId { get; set; }
    internal int Status { get; private set; }
    internal List<object> Messages { get; } = [];

    public void OnSmsConfigurationChange(IMbnSms sms) => ConfigurationChanged.Set();

    public void OnSetSmsConfigurationComplete(IMbnSms sms, uint requestID, HRESULT status) { }

    public void OnSmsSendComplete(IMbnSms sms, uint requestID, HRESULT status) =>
        Finish(requestID, status);

    public void OnSmsReadComplete(IMbnSms sms, MBN_SMS_FORMAT format, SAFEARRAY* readMessages,
        VARIANT_BOOL moreMessages, uint requestID, HRESULT status)
    {
        if (RequestId != 0 && requestID != RequestId) return;
        RequestId = requestID;
        if (status.Value >= 0 && readMessages is not null)
        {
            foreach (var message in NativeArray.ReadUnknown<IMbnSmsReadMsgPdu>(readMessages, false))
            {
                Messages.Add(new { index = message.Index, status = message.Status.ToString(),
                    pdu = Program.Take(message.PduData) });
            }
        }
        Status = status.Value;
        if (status.Value < 0 || moreMessages.Value == 0) Completed.Set();
    }

    public void OnSmsNewClass0Message(IMbnSms sms, MBN_SMS_FORMAT format, SAFEARRAY* messages) { }

    public void OnSmsDeleteComplete(IMbnSms sms, uint requestID, HRESULT status) =>
        Finish(requestID, status);

    public void OnSmsStatusChange(IMbnSms sms) { }

    private void Finish(uint requestId, HRESULT status)
    {
        if (RequestId != 0 && requestId != RequestId) return;
        RequestId = requestId;
        Status = status.Value;
        Completed.Set();
    }
}

internal sealed unsafe class ConnectionPointSubscription : IDisposable
{
    private readonly IConnectionPoint point;
    private readonly uint cookie;

    private ConnectionPointSubscription(IConnectionPoint point, uint cookie)
    {
        this.point = point;
        this.cookie = cookie;
    }

    internal static ConnectionPointSubscription Create<T>(object source, object sink)
    {
        var container = (IConnectionPointContainer)source;
        var iid = typeof(T).GUID;
        container.FindConnectionPoint(&iid, out var point);
        point.Advise(sink, out var cookie);
        return new ConnectionPointSubscription(point, cookie);
    }

    public void Dispose() => point.Unadvise(cookie);
}
