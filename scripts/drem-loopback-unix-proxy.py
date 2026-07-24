#!/usr/bin/env python3
"""Expose one fixed loopback TCP service through a user-only Unix socket."""

import argparse
import os
import socket
import socketserver
import threading


class ProxyHandler(socketserver.BaseRequestHandler):
    def handle(self):
        upstream = socket.create_connection(self.server.upstream, timeout=10)

        def pump(source, target):
            while True:
                data = source.recv(65536)
                if not data:
                    break
                target.sendall(data)
            try:
                target.shutdown(socket.SHUT_WR)
            except OSError:
                pass

        request_to_upstream = threading.Thread(
            target=pump, args=(self.request, upstream), daemon=True
        )
        upstream_to_request = threading.Thread(
            target=pump, args=(upstream, self.request), daemon=True
        )
        try:
            request_to_upstream.start()
            upstream_to_request.start()
            request_to_upstream.join()
            upstream_to_request.join()
        finally:
            upstream.close()


class ProxyServer(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", required=True)
    parser.add_argument("--upstream-host", default="127.0.0.1")
    parser.add_argument("--upstream-port", required=True, type=int)
    args = parser.parse_args()
    if not os.path.isabs(args.socket):
        parser.error("--socket must be absolute")
    os.makedirs(os.path.dirname(args.socket), exist_ok=True)
    if os.path.exists(args.socket):
        os.unlink(args.socket)
    with ProxyServer(args.socket, ProxyHandler) as server:
        os.chmod(args.socket, 0o600)
        server.upstream = (args.upstream_host, args.upstream_port)
        server.serve_forever()


if __name__ == "__main__":
    main()
