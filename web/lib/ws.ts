"use client";

// useFleetStream: keeps the react-query "fleet" cache live.
// Primary path is the /api/ws stream (fleet.update frames); if the socket
// fails, a 15 s poll of /api/fleet takes over until the socket recovers.

import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, getToken, post, clearToken } from "./api";
import type {
  Deployment,
  DeployLogFrame,
  FleetResponse,
  FleetServer,
  FleetUpdateFrame,
} from "./types";

export const FLEET_KEY = ["fleet"] as const;

/** One sparkline sample. */
export interface FleetSample {
  t: number;
  v: number;
}

export function fleetUrl() {
  const tok = getToken() ?? "";
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${window.location.host}/api/ws?token=${encodeURIComponent(tok)}`;
}

export function useFleet() {
  return useQuery({
    queryKey: FLEET_KEY,
    queryFn: () => get<FleetResponse>("/api/fleet"),
    select: (d) => d.servers ?? [],
  });
}

/**
 * History of a numeric series per server, derived from every fleet snapshot
 * that has passed through the cache — sparklines never need their own fetch.
 */
export function useFleetHistory(
  pick: (s: FleetServer) => number | null,
): RefObject<Map<string, FleetSample[]>> {
  const { data: servers } = useFleet();
  const historyRef = useRef<Map<string, FleetSample[]>>(new Map());

  useEffect(() => {
    if (!servers) return;
    const now = Date.now();
    for (const s of servers) {
      const v = pick(s);
      // Copy, never mutate: these arrays are handed to chart components whose
      // state pipeline (immer) deep-freezes them after render.
      const hist = [...(historyRef.current.get(s.id) ?? [])];
      if (v !== null && v !== undefined) {
        hist.push({ t: now, v });
        while (hist.length > 60) hist.shift();
      }
      historyRef.current.set(s.id, hist);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [servers]);

  return historyRef;
}

/** Opens /api/ws and folds fleet.update frames into the fleet cache. */
export function useFleetStream() {
  const qc = useQueryClient();
  const backoff = useRef(1000);

  useEffect(() => {
    let ws: WebSocket | null = null;
    let closed = false;
    let reconnect: ReturnType<typeof setTimeout> | undefined;
    let poll: ReturnType<typeof setInterval> | undefined;

    const startPolling = () => {
      if (poll) return;
      poll = setInterval(() => {
        qc.invalidateQueries({ queryKey: FLEET_KEY });
      }, 15_000);
      qc.invalidateQueries({ queryKey: FLEET_KEY });
    };
    const stopPolling = () => {
      clearInterval(poll);
      poll = undefined;
    };

    const connect = () => {
      if (closed) return;
      if (!getToken()) return; // not authenticated yet; layout guard re-runs us
      try {
        ws = new WebSocket(fleetUrl());
      } catch {
        startPolling();
        reconnect = setTimeout(connect, backoff.current);
        backoff.current = Math.min(backoff.current * 2, 15_000);
        return;
      }
      ws.onopen = () => {
        backoff.current = 1000;
        stopPolling();
      };
      ws.onmessage = (ev) => {
        try {
          const frame = JSON.parse(ev.data as string) as FleetUpdateFrame;
          if (frame.type === "fleet.update" && Array.isArray(frame.servers)) {
            qc.setQueryData<FleetResponse>(FLEET_KEY, { servers: frame.servers });
          }
        } catch {
          /* ignore malformed frames */
        }
      };
      ws.onclose = () => {
        ws = null;
        if (closed) return;
        startPolling();
        reconnect = setTimeout(connect, backoff.current);
        backoff.current = Math.min(backoff.current * 2, 15_000);
      };
      ws.onerror = () => ws?.close();
    };

    connect();
    return () => {
      closed = true;
      clearTimeout(reconnect);
      stopPolling();
      ws?.close();
    };
  }, [qc]);
}

export async function logout() {
  try {
    await post("/api/auth/logout");
  } catch {
    /* token already dead — still clear locally */
  }
  clearToken();
  window.location.href = "/login";
}

// ---------------------------------------------------------------- Phase C4

export interface DeploymentStream {
  lines: string[];
  status: string | null;
  done: boolean;
  error: string | null;
}

/**
 * useDeploymentStream: live log pane feed for one deployment. Connects to
 * /api/deployments/{id}/logs (WS: stored log replayed, then live
 * deploy.log/deploy.status tails). Falls back to polling the deployment
 * status (log replay only) when the socket cannot be established.
 */
export function useDeploymentStream(id: string): DeploymentStream {
  const [state, setState] = useState<DeploymentStream>({
    lines: [],
    status: null,
    done: false,
    error: null,
  });

  useEffect(() => {
    if (!id) return;
    let ws: WebSocket | null = null;
    let closed = false;
    let poll: ReturnType<typeof setInterval> | undefined;
    let reconnect: ReturnType<typeof setTimeout> | undefined;
    let backoff = 1000;

    // Poll fallback: deployment status only (logs arrive on reconnect).
    const startPolling = () => {
      if (poll) return;
      poll = setInterval(() => {
        get<Deployment>(`/api/deployments/${id}`)
          .then((d) =>
            setState((s) =>
              s.done ? s : { ...s, status: d.status, done: isTerminal(d.status) },
            ),
          )
          .catch(() => {});
      }, 5000);
    };
    const stopPolling = () => {
      clearInterval(poll);
      poll = undefined;
    };

    const connect = () => {
      if (closed) return;
      const tok = getToken();
      if (!tok) return;
      const proto = window.location.protocol === "https:" ? "wss" : "ws";
      let sock: WebSocket;
      try {
        sock = new WebSocket(
          `${proto}://${window.location.host}/api/deployments/${id}/logs?token=${encodeURIComponent(tok)}`,
        );
      } catch {
        startPolling();
        reconnect = setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, 15_000);
        return;
      }
      ws = sock;
      sock.onopen = () => {
        backoff = 1000;
        stopPolling();
      };
      sock.onmessage = (ev) => {
        try {
          const f = JSON.parse(ev.data as string) as DeployLogFrame;
          setState((s) => {
            if (f.type === "deploy.log" && typeof f.line === "string") {
              return { ...s, lines: [...s.lines, f.line] };
            }
            if (f.type === "deploy.status" && typeof f.status === "string") {
              return { ...s, status: f.status, done: isTerminal(f.status) };
            }
            if (f.done) return { ...s, done: true };
            return s;
          });
        } catch {
          /* ignore malformed frames */
        }
      };
      sock.onclose = () => {
        ws = null;
        if (closed) return;
        // Terminal deployments get replay + closing done frame; a close after
        // that is final, anything else retries with polling as safety net.
        setState((s) => {
          if (s.done) return s;
          startPolling();
          reconnect = setTimeout(connect, backoff);
          backoff = Math.min(backoff * 2, 15_000);
          return s;
        });
      };
      sock.onerror = () => sock.close();
    };

    connect();
    return () => {
      closed = true;
      clearTimeout(reconnect);
      stopPolling();
      ws?.close();
    };
  }, [id]);

  return state;
}

export function isTerminal(status: string | null | undefined): boolean {
  return status === "success" || status === "failed" || status === "canceled";
}
