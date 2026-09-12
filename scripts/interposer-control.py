#!/usr/bin/env python3
import pathlib
import socket
import sys
import time

path = pathlib.Path(sys.argv[1])
log_path = pathlib.Path(sys.argv[2])
reject_port = sys.argv[3] if len(sys.argv) > 3 else None
delay_port = sys.argv[4] if len(sys.argv) > 4 else None
delay_seconds = float(sys.argv[5]) if len(sys.argv) > 5 else 0
delayed = False
delay_commit_done = False
path.unlink(missing_ok=True)
server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
server.bind(str(path))
server.listen(16)
with server:
    while True:
        connection, _ = server.accept()
        with connection:
            line = b""
            while not line.endswith(b"\n"):
                chunk = connection.recv(256)
                if not chunk:
                    break
                line += chunk
            if not line:
                continue
            text = line.decode("ascii", "replace")
            with log_path.open("a", encoding="ascii") as log:
                log.write(text)
            if text.startswith("RESERVE "):
                fields = text.split()
                if reject_port is not None and len(fields) > 3 and fields[3] == reject_port:
                    connection.sendall(b"ERR 98 address already in use\n")
                else:
                    if not delayed and delay_port is not None and len(fields) > 3 and fields[3] == delay_port:
                        delayed = True
                        time.sleep(delay_seconds)
                    try:
                        connection.sendall(b"OK 1\n")
                    except OSError:
                        pass
            else:
                if text.startswith("COMMIT ") and delayed and not delay_commit_done:
                    delay_commit_done = True
                    time.sleep(delay_seconds)
                try:
                    connection.sendall(b"OK\n")
                except OSError:
                    pass
