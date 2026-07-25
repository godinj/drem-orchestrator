#!/usr/bin/env python3
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
import time


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length))
        usage = {"prompt_tokens": 17, "completion_tokens": 4, "total_tokens": 21}
        if request.get("stream"):
            if not request.get("stream_options", {}).get("include_usage"):
                self.send_error(422, "streaming usage was not requested")
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            chunks = [
                {"id": "canary", "object": "chat.completion.chunk", "created": int(time.time()), "model": request.get("model", "canary"), "choices": [{"index": 0, "delta": {"role": "assistant", "content": "CANVASBENCH_CANARY_OK"}, "finish_reason": None}]},
                {"id": "canary", "object": "chat.completion.chunk", "created": int(time.time()), "model": request.get("model", "canary"), "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": usage},
            ]
            for chunk in chunks:
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
            self.wfile.write(b"data: [DONE]\n\n")
            return
        response = {
            "id": "canary", "object": "chat.completion", "created": int(time.time()),
            "model": request.get("model", "canary"),
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "CANVASBENCH_CANARY_OK"}, "finish_reason": "stop"}],
            "usage": usage,
        }
        raw = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_args):
        return


def main():
    listen = os.environ.get("CANVASBENCH_FAKE_LISTEN", "0.0.0.0:8082")
    host, port = listen.rsplit(":", 1)
    ThreadingHTTPServer((host, int(port)), Handler).serve_forever()


if __name__ == "__main__":
    main()
