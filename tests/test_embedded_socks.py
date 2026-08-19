import socket
import struct
import threading
import unittest

from agent.embedded_socks import SocksServer


def exact(stream, size):
    value = b""
    while len(value) < size:
        value += stream.recv(size - len(value))
    return value


class EmbeddedSocksTests(unittest.TestCase):
    def setUp(self):
        self.server = SocksServer("127.0.0.1", 0, "127.0.0.1")
        self.server.start()

    def tearDown(self):
        self.server.close()

    def negotiate(self, command, host, port):
        control = socket.create_connection(("127.0.0.1", self.server.port), 2)
        control.settimeout(2)
        control.sendall(b"\x05\x01\x00")
        self.assertEqual(exact(control, 2), b"\x05\x00")
        control.sendall(b"\x05" + bytes([command]) + b"\x00\x01" +
                        socket.inet_aton(host) + struct.pack("!H", port))
        reply = exact(control, 10)
        self.assertEqual(reply[:2], b"\x05\x00")
        return control, (socket.inet_ntoa(reply[4:8]), struct.unpack("!H", reply[8:10])[0])

    def test_tcp_connect_relays_bytes(self):
        echo = socket.socket()
        echo.bind(("127.0.0.1", 0)); echo.listen(1)
        def serve():
            client, _ = echo.accept()
            with client:
                client.sendall(client.recv(64))
        threading.Thread(target=serve, daemon=True).start()
        control, _ = self.negotiate(1, "127.0.0.1", echo.getsockname()[1])
        with control:
            control.sendall(b"mdd-tcp")
            self.assertEqual(exact(control, 7), b"mdd-tcp")
        echo.close()

    def test_udp_associate_relays_datagrams(self):
        echo = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        echo.bind(("127.0.0.1", 0)); echo.settimeout(2)
        def serve():
            value, peer = echo.recvfrom(64)
            echo.sendto(value, peer)
        threading.Thread(target=serve, daemon=True).start()
        control, relay = self.negotiate(3, "0.0.0.0", 0)
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); udp.settimeout(2)
        target = b"\x00\x00\x00\x01" + socket.inet_aton("127.0.0.1") + \
            struct.pack("!H", echo.getsockname()[1]) + b"mdd-udp"
        udp.sendto(target, relay)
        response, _ = udp.recvfrom(256)
        self.assertTrue(response.endswith(b"mdd-udp"))
        udp.close(); control.close(); echo.close()


if __name__ == "__main__":
    unittest.main()
