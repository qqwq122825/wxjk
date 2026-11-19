"""Local-only HTTPS/WSS peer for simulator integration tests, not a backend."""
from datetime import datetime, timedelta, timezone
from email.parser import BytesParser
from email.policy import default
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import base64
import hashlib
import json
import socket
import ssl
import struct
import threading


def timestamp(seconds=0):
    return (datetime.now(timezone.utc) + timedelta(seconds=seconds)).isoformat(timespec="seconds")


class MockDeviceServer:
    def __init__(self, certificate, key):
        self.lock = threading.RLock()
        self.upgrades = 0
        self.uploads = []
        self.registrations = 0
        self.active_peer = None
        self.failures = []
        self.start = {
            "type": "command.dispatch", "command": "START_IMAGE_SESSION",
            "command_id": "mock-start", "created_at": timestamp(), "expires_at": timestamp(180),
            "assignment_id": "mock-assignment", "assignment_version": 1,
            "parameters": {"session_id": "mock-phone-task", "session_version": 1,
                           "capture_source": "uikit_window", "capture_scope": "active_window", "interval_ms": 3000},
        }
        self.stop = {
            "type": "command.dispatch", "command": "STOP_IMAGE_SESSION",
            "command_id": "mock-stop", "created_at": timestamp(), "expires_at": timestamp(180),
            "parameters": {"session_id": "mock-phone-task", "session_version": 2},
        }
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def reply(self, status, value):
                body = json.dumps(value).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                data = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                if self.path.endswith("/register"):
                    payload = json.loads(data)
                    if payload.get("enrollment_token") not in (None, "test-enrollment") or not payload.get("registration_secret"):
                        self.reply(403, {"error": "enrollment"})
                        return
                    with owner.lock:
                        owner.registrations += 1
                    self.reply(200, {"device_id": "mock-device", "device_token": "mock-device-token", "server_time": timestamp()})
                    return
                if self.path.endswith("/images"):
                    if self.headers.get("Authorization") != "Bearer mock-device-token":
                        self.reply(401, {})
                        return
                    message = BytesParser(policy=default).parsebytes(
                        f"Content-Type: {self.headers['Content-Type']}\r\nMIME-Version: 1.0\r\n\r\n".encode() + data
                    )
                    parts = {part.get_param("name", header="content-disposition"): part.get_payload(decode=True)
                             for part in message.iter_parts()}
                    metadata = json.loads(parts["metadata"])
                    if not parts["image"].startswith(b"\x89PNG\r\n\x1a\n"):
                        owner.failures.append("invalid PNG upload")
                    with owner.lock:
                        owner.uploads.append(metadata)
                        total = len(owner.uploads)
                        peer = owner.active_peer
                    self.reply(201, {"report_id": metadata["report_id"], "status": "succeeded"})
                    if total == 1 and peer:
                        # Cut the network after a committed frame. The phone
                        # task must keep its identity and reconnect without a viewer.
                        try:
                            peer.connection.shutdown(socket.SHUT_RDWR)
                        except OSError:
                            pass
                    elif total == 3 and peer:
                        peer.send_json(owner.stop)
                    return
                self.reply(404, {})

            def do_GET(self):
                if not self.path.endswith("/ws"):
                    self.reply(404, {})
                    return
                if self.path.startswith("/unauthorized/"):
                    self.reply(401, {})
                    return
                with owner.lock:
                    owner.upgrades += 1
                    number = owner.upgrades
                if number == 1:
                    self.reply(503, {"error": "intentional initial failure"})
                    return
                if self.headers.get("Authorization") != "Bearer mock-device-token":
                    self.reply(401, {})
                    return
                accept = base64.b64encode(hashlib.sha1(
                    (self.headers["Sec-WebSocket-Key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()
                ).digest()).decode()
                self.send_response(101)
                self.send_header("Upgrade", "websocket")
                self.send_header("Connection", "Upgrade")
                self.send_header("Sec-WebSocket-Accept", accept)
                self.end_headers()
                self.write_lock = threading.Lock()
                self.close_connection = True
                self.connection.settimeout(35)
                with owner.lock:
                    owner.active_peer = self
                self.send_json({"type": "device.welcome", "connection_id": f"mock-{number}"})
                try:
                    while True:
                        frame = self.read_frame()
                        if frame is None:
                            break
                        opcode, payload = frame
                        if opcode == 8:
                            break
                        if opcode == 9:
                            self.send_frame(10, payload)
                            continue
                        if opcode != 1:
                            continue
                        message = json.loads(payload)
                        kind = message.get("type")
                        if kind == "device.hello":
                            stale = {
                                "type": "command.dispatch", "command": "GET_IMAGE", "command_id": "mock-old-assignment",
                                "expires_at": timestamp(120), "assignment_id": "old-assignment", "assignment_version": 0,
                                "parameters": {"capture_source": "uikit_window", "capture_scope": "active_window"},
                            }
                            self.send_json({"type": "device.sync", "assignment": {"id": "mock-assignment", "version": 1},
                                            "commands": [stale, owner.start]})
                        elif kind == "command.result":
                            self.send_json({"type": "command.result_ack", "command_id": message["command_id"]})
                            if message["command_id"] == "mock-stop":
                                self.send_json({"type": "device.disabled"})
                except (OSError, ValueError):
                    pass

            def read_exactly(self, size):
                data = self.rfile.read(size)
                if len(data) != size:
                    raise OSError("connection ended")
                return data

            def read_frame(self):
                header = self.rfile.read(2)
                if len(header) != 2:
                    return None
                opcode, length = header[0] & 15, header[1] & 127
                if length == 126:
                    length = struct.unpack("!H", self.read_exactly(2))[0]
                elif length == 127:
                    length = struct.unpack("!Q", self.read_exactly(8))[0]
                if length > 65536:
                    raise OSError("oversized test frame")
                mask = self.read_exactly(4) if header[1] & 128 else None
                payload = self.read_exactly(length)
                if mask:
                    payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
                return opcode, payload

            def send_frame(self, opcode, payload):
                header = bytes([128 | opcode])
                if len(payload) < 126:
                    header += bytes([len(payload)])
                else:
                    header += bytes([126]) + struct.pack("!H", len(payload))
                with self.write_lock:
                    self.wfile.write(header + payload)
                    self.wfile.flush()

            def send_json(self, value):
                self.send_frame(1, json.dumps(value).encode())

        self.http = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        self.http.socket = context.wrap_socket(self.http.socket, server_side=True)
        self.thread = threading.Thread(target=self.http.serve_forever, daemon=True)
        self.thread.start()
        self.URL = f"https://localhost:{self.http.server_address[1]}"

    def close(self):
        self.http.shutdown()
        self.http.server_close()
