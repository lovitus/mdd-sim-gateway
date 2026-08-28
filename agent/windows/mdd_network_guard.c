#define _WIN32_WINNT 0x0A00
#include <winsock2.h>
#include <windows.h>
#include <fwpmu.h>
#include <netioapi.h>
#include <cfgmgr32.h>
#include <stdio.h>
#include <wchar.h>

/* Dynamic WFP policy: permit the packaged MDD Agent on one interface, block every other app.
 * The filters disappear automatically when this process or its parent exits. */
static const GUID MDD_SUBLAYER =
    {0x66d713ea,0x359a,0x4c3d,{0x9a,0xa0,0x4f,0xe4,0xc8,0x9f,0xb5,0x42}};

/* MinGW's WFP declarations lag the Windows SDK on some installations.  Keep the
 * SDK-defined GUID values local so the same source builds with MSVC and MinGW. */
static const GUID MDD_LAYER_ALE_AUTH_CONNECT_V4 =
    {0xc38d57d1,0x05a7,0x4c33,{0x90,0x4f,0x7f,0xbc,0xee,0xe6,0x0e,0x82}};
static const GUID MDD_LAYER_ALE_AUTH_CONNECT_V6 =
    {0x4a72393b,0x319f,0x44bc,{0x84,0xc3,0xba,0x54,0xdc,0xb3,0xb6,0xb4}};
static const GUID MDD_CONDITION_IP_LOCAL_INTERFACE =
    {0x4cd62a49,0x59c3,0x4969,{0xb7,0xf3,0xbd,0xa5,0xd3,0x28,0x90,0xa4}};
static const GUID MDD_CONDITION_ALE_APP_ID =
    {0xd78e1e87,0x8644,0x4ea5,{0x94,0x37,0xd8,0x09,0xec,0xef,0xc9,0x71}};

#ifndef FWPM_SESSION_FLAG_DYNAMIC
#define FWPM_SESSION_FLAG_DYNAMIC 0x00000001
#endif
#ifndef FWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT
#define FWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT 0x00000008
#endif

static void json_error(const char *message, DWORD code) {
    printf("{\"ready\":false,\"backend\":\"windows-wfp\",\"error\":\"%s (%lu)\"}\n",
           message, (unsigned long)code);
    fflush(stdout);
}

static DWORD add_filter(HANDLE engine, const GUID *layer, const GUID *sublayer,
                        UINT64 *luid, FWP_BYTE_BLOB *app_id, int permit,
                        UINT64 *filter_id) {
    FWPM_FILTER_CONDITION0 conditions[2] = {0};
    conditions[0].fieldKey = MDD_CONDITION_IP_LOCAL_INTERFACE;
    conditions[0].matchType = FWP_MATCH_EQUAL;
    conditions[0].conditionValue.type = FWP_UINT64;
    conditions[0].conditionValue.uint64 = luid;
    if (permit) {
        conditions[1].fieldKey = MDD_CONDITION_ALE_APP_ID;
        conditions[1].matchType = FWP_MATCH_EQUAL;
        conditions[1].conditionValue.type = FWP_BYTE_BLOB_TYPE;
        conditions[1].conditionValue.byteBlob = app_id;
    }
    FWPM_FILTER0 filter = {0};
    filter.displayData.name = permit ? L"MDD cellular agent permit" : L"MDD cellular isolation block";
    filter.layerKey = *layer;
    filter.subLayerKey = *sublayer;
    filter.weight.type = FWP_UINT8;
    filter.weight.uint8 = permit ? 15 : 14;
    filter.numFilterConditions = permit ? 2 : 1;
    filter.filterCondition = conditions;
    filter.action.type = permit ? FWP_ACTION_PERMIT : FWP_ACTION_BLOCK;
    if (!permit) filter.flags = FWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT;
    DWORD status = FwpmFilterAdd0(engine, &filter, NULL, filter_id);
    if (status != ERROR_SUCCESS) return status;
    FWPM_FILTER0 *installed = NULL;
    status = FwpmFilterGetById0(engine, *filter_id, &installed);
    if (status == ERROR_SUCCESS &&
        (!installed || installed->action.type != filter.action.type ||
         !IsEqualGUID(&installed->layerKey, layer))) status = ERROR_INVALID_DATA;
    if (installed) FwpmFreeMemory0((void**)&installed);
    return status;
}

static int filters_alive(HANDLE engine, const UINT64 *filter_ids, int count) {
    for (int i = 0; i < count; i++) {
        FWPM_FILTER0 *installed = NULL;
        DWORD status = FwpmFilterGetById0(engine, filter_ids[i], &installed);
        if (installed) FwpmFreeMemory0((void**)&installed);
        if (status != ERROR_SUCCESS) return 0;
    }
    return 1;
}

static void disconnect_interface(const wchar_t *alias) {
    wchar_t command[1024];
    if (swprintf(command, 1024,
                 L"netsh.exe mbn disconnect interface=\"%ls\"", alias) < 0) return;
    STARTUPINFOW startup = {0};
    PROCESS_INFORMATION process = {0};
    startup.cb = sizeof(startup);
    startup.dwFlags = STARTF_USESHOWWINDOW;
    startup.wShowWindow = SW_HIDE;
    if (CreateProcessW(NULL, command, NULL, NULL, FALSE, CREATE_NO_WINDOW,
                       NULL, NULL, &startup, &process)) {
        WaitForSingleObject(process.hProcess, 15000);
        CloseHandle(process.hThread);
        CloseHandle(process.hProcess);
    }
}

static int signal_release_event(const wchar_t *event_name) {
    HANDLE event = OpenEventW(EVENT_MODIFY_STATE, FALSE, event_name);
    if (!event) {
        json_error("cannot open PnP lease release event", GetLastError());
        return 12;
    }
    BOOL ok = SetEvent(event);
    CloseHandle(event);
    if (!ok) {
        json_error("cannot signal PnP lease release event", GetLastError());
        return 13;
    }
    printf("{\"ok\":true,\"backend\":\"windows-pnp-lease\"}\n");
    return 0;
}

static int lease_device(const wchar_t *device_id, DWORD parent_pid,
                        const wchar_t *release_event_name) {
    HANDLE parent = OpenProcess(SYNCHRONIZE, FALSE, parent_pid);
    if (!parent) {
        json_error("cannot monitor agent process", GetLastError());
        return 7;
    }
    HANDLE release_event = CreateEventW(NULL, FALSE, FALSE, release_event_name);
    if (!release_event) {
        json_error("cannot create PnP lease release event", GetLastError());
        CloseHandle(parent);
        return 7;
    }
    DEVINST instance = 0;
    CONFIGRET result = CM_Locate_DevNodeW(&instance, (DEVINSTID_W)device_id,
                                          CM_LOCATE_DEVNODE_NORMAL);
    if (result != CR_SUCCESS) {
        json_error("cannot locate PnP device", result);
        CloseHandle(release_event);
        CloseHandle(parent);
        return 8;
    }
    /* No CM_DISABLE_PERSIST: an unexpected power cycle restores the device. */
    result = CM_Disable_DevNode(instance, 0);
    if (result != CR_SUCCESS) {
        json_error("cannot lease PnP device", result);
        CloseHandle(release_event);
        CloseHandle(parent);
        return 9;
    }
    ULONG device_status = 0, problem = 0;
    result = CM_Get_DevNode_Status(&device_status, &problem, instance, 0);
    if (result != CR_SUCCESS || problem != CM_PROB_DISABLED) {
        CM_Enable_DevNode(instance, 0);
        json_error("PnP lease postcondition failed",
                   result == CR_SUCCESS ? ERROR_INVALID_STATE : result);
        CloseHandle(release_event);
        CloseHandle(parent);
        return 10;
    }
    printf("{\"ready\":true,\"backend\":\"windows-pnp-lease\",\"problem_code\":%lu}\n",
           (unsigned long)problem);
    fflush(stdout);
    HANDLE waits[2] = {parent, release_event};
    WaitForMultipleObjects(2, waits, FALSE, INFINITE);
    result = CM_Enable_DevNode(instance, 0);
    CloseHandle(release_event);
    CloseHandle(parent);
    return result == CR_SUCCESS ? 0 : 11;
}

int wmain(int argc, wchar_t **argv) {
    const wchar_t *alias = NULL, *application = NULL, *control_application = NULL,
                  *compat_application = NULL, *lease_device_id = NULL,
                  *release_event = NULL, *signal_event = NULL;
    DWORD parent_pid = 0;
    for (int i = 1; i + 1 < argc; i++) {
        if (!wcscmp(argv[i], L"--interface")) alias = argv[++i];
        else if (!wcscmp(argv[i], L"--executable")) application = argv[++i];
        else if (!wcscmp(argv[i], L"--control-executable")) control_application = argv[++i];
        else if (!wcscmp(argv[i], L"--compat-executable")) compat_application = argv[++i];
        else if (!wcscmp(argv[i], L"--lease-device")) lease_device_id = argv[++i];
        else if (!wcscmp(argv[i], L"--release-event")) release_event = argv[++i];
        else if (!wcscmp(argv[i], L"--signal-event")) signal_event = argv[++i];
        else if (!wcscmp(argv[i], L"--pid")) parent_pid = wcstoul(argv[++i], NULL, 10);
    }
    if (signal_event)
        return signal_release_event(signal_event);
    if (lease_device_id && parent_pid && release_event)
        return lease_device(lease_device_id, parent_pid, release_event);
    if (!alias || !application || !parent_pid) {
        json_error("interface, executable and pid are required", ERROR_INVALID_PARAMETER);
        return 2;
    }
    NET_LUID interface_luid = {0};
    DWORD status = ConvertInterfaceAliasToLuid(alias, &interface_luid);
    if (status != NO_ERROR) { json_error("cannot resolve cellular interface", status); return 3; }

    FWP_BYTE_BLOB *app_id = NULL;
    status = FwpmGetAppIdFromFileName0(application, &app_id);
    if (status != ERROR_SUCCESS) { json_error("cannot normalize agent executable", status); return 4; }
    FWP_BYTE_BLOB *control_app_id = NULL;
    if (control_application) {
        status = FwpmGetAppIdFromFileName0(control_application, &control_app_id);
        if (status != ERROR_SUCCESS) {
            json_error("cannot normalize control executable", status);
            FwpmFreeMemory0((void**)&app_id);
            return 4;
        }
    }
    FWP_BYTE_BLOB *compat_app_id = NULL;
    if (compat_application) {
        status = FwpmGetAppIdFromFileName0(compat_application, &compat_app_id);
        if (status != ERROR_SUCCESS) {
            json_error("cannot normalize compatibility executable", status);
            if (control_app_id) FwpmFreeMemory0((void**)&control_app_id);
            FwpmFreeMemory0((void**)&app_id);
            return 4;
        }
    }

    FWPM_SESSION0 session = {0};
    session.displayData.name = L"MDD cellular isolation session";
    session.flags = FWPM_SESSION_FLAG_DYNAMIC;
    HANDLE engine = NULL;
    status = FwpmEngineOpen0(NULL, RPC_C_AUTHN_WINNT, NULL, &session, &engine);
    if (status != ERROR_SUCCESS) { json_error("cannot open WFP engine", status); FwpmFreeMemory0((void**)&app_id); return 5; }

    /* Dynamic objects belong to the engine session that created them. If two guard
     * generations overlap during restart, sharing one dynamic sublayer makes the second
     * session fail with FWP_E_WRONG_SESSION. A process PID cannot be reused while the old
     * helper is alive, so every concurrent dynamic session receives its own key. */
    GUID sublayer_key = MDD_SUBLAYER;
    sublayer_key.Data1 ^= GetCurrentProcessId();
    FWPM_SUBLAYER0 sublayer = {0};
    sublayer.subLayerKey = sublayer_key;
    sublayer.displayData.name = L"MDD cellular isolation";
    sublayer.weight = 0xFFFF;
    status = FwpmSubLayerAdd0(engine, &sublayer, NULL);
    if (status != ERROR_SUCCESS && status != FWP_E_ALREADY_EXISTS) {
        json_error("cannot add WFP sublayer", status); goto done;
    }
    UINT64 luid_value = interface_luid.Value;
    UINT64 filter_ids[8] = {0};
    int filter_count = 0;
    const GUID *layers[] = {&MDD_LAYER_ALE_AUTH_CONNECT_V4,
                            &MDD_LAYER_ALE_AUTH_CONNECT_V6};
    for (int i = 0; i < 2; i++) {
        status = add_filter(engine, layers[i], &sublayer_key, &luid_value, app_id, 1,
                            &filter_ids[filter_count]);
        if (status != ERROR_SUCCESS) { json_error("cannot add agent permit filter", status); goto done; }
        filter_count++;
        if (control_app_id) {
            status = add_filter(engine, layers[i], &sublayer_key, &luid_value,
                                control_app_id, 1,
                                &filter_ids[filter_count]);
            if (status != ERROR_SUCCESS) { json_error("cannot add control permit filter", status); goto done; }
            filter_count++;
        }
        if (compat_app_id) {
            status = add_filter(engine, layers[i], &sublayer_key, &luid_value,
                                compat_app_id, 1,
                                &filter_ids[filter_count]);
            if (status != ERROR_SUCCESS) { json_error("cannot add compatibility permit filter", status); goto done; }
            filter_count++;
        }
        status = add_filter(engine, layers[i], &sublayer_key, &luid_value, app_id, 0,
                            &filter_ids[filter_count]);
        if (status != ERROR_SUCCESS) { json_error("cannot add isolation block filter", status); goto done; }
        filter_count++;
    }

    printf("{\"ready\":true,\"backend\":\"windows-wfp\"}\n");
    fflush(stdout);
    {
        HANDLE parent = OpenProcess(SYNCHRONIZE, FALSE, parent_pid);
        if (!parent) { status = GetLastError(); json_error("cannot monitor agent process", status); }
        else {
            DWORD wait_status;
            status = ERROR_SUCCESS;
            while ((wait_status = WaitForSingleObject(parent, 500)) == WAIT_TIMEOUT) {
                if (!filters_alive(engine, filter_ids, filter_count)) {
                    status = ERROR_INVALID_STATE;
                    break;
                }
            }
            CloseHandle(parent);
            /* Remove the data path before closing the dynamic WFP session. */
            disconnect_interface(alias);
            if (wait_status == WAIT_FAILED) status = GetLastError();
        }
    }

done:
    if (engine) FwpmEngineClose0(engine);
    if (compat_app_id) FwpmFreeMemory0((void**)&compat_app_id);
    if (control_app_id) FwpmFreeMemory0((void**)&control_app_id);
    if (app_id) FwpmFreeMemory0((void**)&app_id);
    return status == ERROR_SUCCESS ? 0 : 6;
}
