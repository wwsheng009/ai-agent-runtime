"""ProxyCommand bridge for ssh-client / sftp-client (works on Windows too).

Speaks stdio on one side and CONNECTs to the target through a local HTTP proxy on
the other, so

  ssh-client  -o ProxyCommand="python scripts/ssh-connect-proxy.py %h %p" ...
  sftp-client -o ProxyCommand="python scripts/ssh-connect-proxy.py %h %p" ...

work without `nc` or `connect.exe` (docs/tools/ssh-sftp-clients-usage.md §2.3/§2.4).
The SSH client expands %h/%p into the target host and port and execs this script.

Usage (normally invoked by the client, not by hand):
  python scripts/ssh-connect-proxy.py <host> <port> [proxy_host:port]

The proxy defaults to 127.0.0.1:10810.
"""
import os
import socket
import sys
import threading

DEFAULT_PROXY = "127.0.0.1:10810"


def stdin_to_socket(up: socket.socket) -> None:
    try:
        while True:
            read1 = getattr(sys.stdin.buffer, "read1", None)
            data = read1(65536) if read1 else sys.stdin.buffer.read(4096)
            if not data:
                try:
                    up.shutdown(socket.SHUT_WR)
                except Exception:
                    pass
                return
            up.sendall(data)
    except Exception:
        return


def socket_to_stdout(up: socket.socket) -> None:
    try:
        while True:
            data = up.recv(65536)
            if not data:
                break
            sys.stdout.buffer.write(data)
            sys.stdout.buffer.flush()
    except Exception:
        pass
    finally:
        try:
            sys.stdout.buffer.flush()
        except Exception:
            pass
        os._exit(0)


def main() -> int:
    if len(sys.argv) < 3:
        sys.stderr.write("usage: ssh-connect-proxy.py <host> <port> [proxy_host:port]\n")
        return 2
    host = sys.argv[1]
    port = int(sys.argv[2])
    proxy = sys.argv[3] if len(sys.argv) > 3 else DEFAULT_PROXY
    proxy_host, _, proxy_port = proxy.rpartition(":")

    try:
        up = socket.create_connection((proxy_host, int(proxy_port)), timeout=15)
    except Exception as exc:  # noqa: BLE001
        sys.stderr.write("connect to proxy %s failed: %s\n" % (proxy, exc))
        return 3
    request = (
        "CONNECT %s:%d HTTP/1.1\r\nHost: %s:%d\r\n"
        "Proxy-Connection: keep-alive\r\n\r\n" % (host, port, host, port)
    )
    try:
        up.sendall(request.encode())
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = up.recv(4096)
            if not chunk:
                sys.stderr.write("proxy closed before CONNECT completed\n")
                return 4
            buf += chunk
    except Exception as exc:  # noqa: BLE001
        sys.stderr.write("CONNECT to %s:%d via %s failed: %s\n" % (host, port, proxy, exc))
        return 5

    head, _, rest = buf.partition(b"\r\n\r\n")
    status = head.split(b"\r\n", 1)[0]
    if b" 200" not in status:
        sys.stderr.write("proxy refused CONNECT %s:%d: %r\n" % (host, port, status))
        return 6
    if rest:
        sys.stdout.buffer.write(rest)
        sys.stdout.buffer.flush()

    threading.Thread(target=stdin_to_socket, args=(up,), daemon=True).start()
    socket_to_stdout(up)
    return 0


if __name__ == "__main__":
    sys.exit(main())
