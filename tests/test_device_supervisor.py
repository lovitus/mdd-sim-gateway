from types import SimpleNamespace

from agent.device_supervisor import MacUsbModemDiscovery


def test_usb_modem_discovery_uses_serial_for_physical_identity_and_location_for_generation():
    output = (
        "device vid=2c7c pid=0125 bus=1 address=4 serial=SERIAL-A\n"
        "device vid=1234 pid=5678 bus=2 address=8 serial=\n")
    discovery = MacUsbModemDiscovery(
        "/bundle/mdd-cellular-io",
        runner=lambda *_args, **_kwargs: SimpleNamespace(
            returncode=0, stdout=output, stderr=""))

    first, second = discovery.enumerate()

    assert first.physical_identity == "usb:2c7c:0125:SERIAL-A"
    assert first.generation.endswith("@1:4")
    assert second.physical_identity == "usb:1234:5678:location:2:8"
    assert second.generation.endswith("@2:8")


def test_discovery_excludes_generations_already_owned_by_runtime():
    calls = []
    discovery = MacUsbModemDiscovery(
        "/bundle/mdd-cellular-io",
        runner=lambda command, **_kwargs: calls.append(command) or SimpleNamespace(
            returncode=0, stdout="", stderr=""))

    assert discovery.enumerate(exclude={(3, 9), (1, 4)}) == []
    assert calls == [["/bundle/mdd-cellular-io", "--list",
                      "--exclude", "1:4", "--exclude", "3:9"]]
