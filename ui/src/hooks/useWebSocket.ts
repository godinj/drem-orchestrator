import { useEffect, useRef, useState, useCallback } from "react";
import type { WSEvent } from "../types";

interface UseWebSocketResult {
  connected: boolean;
  reconnecting: boolean;
}

export function useWebSocket(
  projectId: string | null,
  onEvent: (event: WSEvent) => void,
): UseWebSocketResult {
  const [connected, setConnected] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const retriesRef = useRef(0);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const onEventRef = useRef(onEvent);

  // Keep the callback ref fresh without causing reconnects
  useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);

  const connect = useCallback(() => {
    if (!projectId) return;

    // Determine WebSocket URL based on current location
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const host = window.location.host;
    const url = `${protocol}//${host}/api/ws/${projectId}`;

    const ws = new WebSocket(url);
    wsRef.current = ws;

    ws.onopen = () => {
      setConnected(true);
      setReconnecting(false);
      retriesRef.current = 0;
    };

    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data) as WSEvent;
        onEventRef.current(data);
      } catch (err) {
        console.error("Failed to parse WebSocket message:", err);
      }
    };

    ws.onclose = () => {
      setConnected(false);
      wsRef.current = null;

      // Auto-reconnect with exponential backoff
      const delay = Math.min(1000 * Math.pow(2, retriesRef.current), 30000);
      retriesRef.current += 1;
      setReconnecting(true);

      timeoutRef.current = setTimeout(() => {
        connect();
      }, delay);
    };

    ws.onerror = (err) => {
      console.error("WebSocket error:", err);
      ws.close();
    };
  }, [projectId]);

  useEffect(() => {
    connect();

    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      if (wsRef.current) {
        wsRef.current.close();
      }
    };
  }, [connect]);

  return { connected, reconnecting };
}
