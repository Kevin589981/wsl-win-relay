#!/usr/bin/env python3
import pathlib
import socket
import sys

path = pathlib.Path(sys.argv[1])
log_path = pathlib.Path(sys.argv[2])
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
                connection.sendall(b"OK 1\n")
            else:
                connection.sendall(b"OK\n")
