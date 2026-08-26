<script lang="ts">
  import { tick, untrack } from "svelte";
  import { api } from "../lib/api";
  import { run, select, ui } from "../lib/commands.svelte";
  import { act, agentsIn, fleet } from "../lib/state.svelte";
  import { canvasLayout } from "../lib/layout.svelte";
  import {
    arrowBetween,
    boxAt,
    centeredOn,
    fitView,
    frameAround,
    frameMin,
    frameOf,
    frameTitle,
    homeView,
    intersects,
    panBy,
    showing,
    simplified,
    spanning,
    stagePoint,
    strokeOf,
    zoomAt,
    type Box,
    type Shape,
    type View,
  } from "../lib/canvas";
  import CanvasDrawings from "./CanvasDrawings.svelte";
  import CanvasLinks from "./CanvasLinks.svelte";
  import CanvasFrame from "./CanvasFrame.svelte";
  import CanvasTile from "./CanvasTile.svelte";
  import CanvasTools from "./CanvasTools.svelte";
  import { isUrgent } from "../lib/types";

  let { onopen }: { onopen: () => void } = $props();

  const agents = $derived(agentsIn(fleet.workspaceID));
  // A fresh store per workspace: switching workspaces in the rail swaps
  // the whole arrangement, each remembered separately.
  const layout = $derived(canvasLayout(fleet.workspaceID));

  // The tile with the keyboard: the selected agent's, while walked in.
  // The canvas types in place, so the walk that puts the keyboard in
  // the roster's pane puts it here instead when this is the view.
  const focused = $derived(ui.walkedIn ? fleet.selectedID : "");

  let clip = $state<HTMLDivElement>();
  let view = $state<View>(homeView);

  /**
   * A drag in flight: the offset it has travelled, what is doing the
   * travelling, and every tile it carries. Held here rather than in the
   * tile because a drag can carry more than one tile: dragging a
   * selected tile carries the whole selection, and dragging a frame
   * carries whatever it holds. Every carried tile shows the same offset
   * until the hand lets go. A tile outside the selection drags alone
   * and leaves the selection as it was — dragging is not selecting,
   * and a drag must never move the cursor, which is where the keyboard
   * is.
   *
   * Who is carried is decided once, when the drag begins, from the
   * committed boxes: a frame's members are the tiles whose centres it
   * held then, and a tile inside a locked frame is never carried.
   */
  let drift = $state<{
    kind: "tile" | "frame";
    id: string;
    members: Set<string>;
    dx: number;
    dy: number;
  } | null>(null);

  const carried = (id: string): boolean => drift?.members.has(id) ?? false;

  const membersOf = (kind: "tile" | "frame", id: string): Set<string> => {
    const free = (agentID: string) => !lockedTile(agentID);
    if (kind === "frame") {
      return new Set(
        agents
          .filter((a) => frameOfTile(a.id) === id && free(a.id))
          .map((a) => a.id),
      );
    }
    if (ui.selection.has(id)) {
      return new Set([...ui.selection].filter(free));
    }
    return new Set(free(id) ? [id] : []);
  };

  /** Where a tile is drawn: its box, plus the drift if it is carried. */
  const shownBox = (id: string): Box => {
    const box = layout.tiles[id];
    if (!drift || !carried(id)) return box;
    return { ...box, x: box.x + drift.dx, y: box.y + drift.dy };
  };

  /** Where a frame is drawn: its box, plus its own drift. */
  const shownFrame = (id: string): Box => {
    const frame = layout.frames[id];
    if (!drift || drift.kind !== "frame" || drift.id !== id) return frame;
    return { ...frame, x: frame.x + drift.dx, y: frame.y + drift.dy };
  };

  const drifted = (kind: "tile" | "frame", id: string, dx: number, dy: number) => {
    drift =
      drift && drift.kind === kind && drift.id === id
        ? { ...drift, dx: drift.dx + dx, dy: drift.dy + dy }
        : { kind, id, members: membersOf(kind, id), dx, dy };
  };

  /** The hand let go: everything carried lands where it is shown, or
   *  nowhere if the gesture was cancelled. */
  const landed = (commit: boolean) => {
    if (!drift) return;
    if (commit) {
      for (const agent of agents) {
        if (carried(agent.id)) layout.put(agent.id, shownBox(agent.id));
      }
      if (drift.kind === "frame") {
        layout.putFrame(drift.id, {
          ...layout.frames[drift.id],
          ...shownFrame(drift.id),
        });
      }
    }
    drift = null;
  };

  /**
   * The frame tool, picked with a selection in hand, frames the
   * selection on the spot — Excalidraw's F does the same — and hands
   * back the select tool. With nothing selected it stays in hand and
   * the next drag draws the frame.
   */
  $effect(() => {
    if (ui.tool !== "frame" || ui.selection.size === 0) return;
    const held = [...ui.selection]
      .map((id) => layout.tiles[id])
      .filter((box): box is Box => box !== undefined);
    if (held.length === 0) return;
    layout.addFrame(frameAround(held));
    ui.tool = "select";
  });

  /**
   * The drawing tools, on an overlay that takes every press while one
   * is in hand — a stroke has to be able to cross a tile without the
   * tile taking it. Each gesture is one shape: a rectangle, an ellipse
   * or a frame from the band it spans; a line or an arrow from its two
   * ends; a pencil stroke from every point along the way. Letting go
   * commits it and hands back the select tool, which is what Excalidraw
   * does and what a hand expects: draw one thing, then deal with it.
   * Text is a click, and the typing happens where the click was.
   */
  let sketch = $state<{
    pointer: number;
    kind: "rect" | "ellipse" | "frame" | "line" | "arrow" | "pencil";
    points: Array<[number, number]>;
  } | null>(null);

  const draft = $derived.by((): Shape | null => {
    if (!sketch) return null;
    const { kind, points } = sketch;
    if (kind === "rect" || kind === "ellipse" || kind === "frame") {
      const band = spanning(
        { x: points[0][0], y: points[0][1] },
        { x: points[points.length - 1][0], y: points[points.length - 1][1] },
      );
      return { kind: kind === "frame" ? "rect" : kind, ...band };
    }
    if (kind === "pencil") return strokeOf(kind, points);
    return strokeOf(kind, [points[0], points[points.length - 1]]);
  });

  /** The text being typed, at the point clicked. */
  let composing = $state<{ x: number; y: number; text: string } | null>(null);
  let composer = $state<HTMLTextAreaElement>();
  $effect(() => {
    if (composing && composer) composer.focus();
  });
  const commitText = () => {
    if (!composing) return;
    const text = composing.text.trim();
    if (text) layout.addShape({ kind: "text", x: composing.x, y: composing.y, w: 0, h: 0, text });
    composing = null;
    ui.tool = "select";
  };
  const composerKey = (event: KeyboardEvent) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      commitText();
    } else if (event.key === "Escape") {
      event.preventDefault();
      composing = null;
      ui.tool = "select";
    }
  };

  const toolDown = (event: PointerEvent) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const at = pointOf(event);
    if (ui.tool === "hand") {
      panning = { pointer: event.pointerId, x: event.clientX, y: event.clientY };
      (event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId);
      return;
    }
    if (ui.tool === "text") {
      if (composing) commitText();
      composing = { x: at.x, y: at.y, text: "" };
      return;
    }
    if (ui.tool === "select") return;
    // The arrow tool, on a tile, draws a link: the arrow that means
    // something. Off a tile it draws an arrow.
    if (ui.tool === "arrow") {
      const from = boxAt(tileBoxes(), at);
      if (from) {
        linking = { pointer: event.pointerId, from, to: at };
        (event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId);
        return;
      }
    }
    (event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId);
    sketch = { pointer: event.pointerId, kind: ui.tool, points: [[at.x, at.y]] };
  };
  const toolMove = (event: PointerEvent) => {
    if (linkMoved(event)) return;
    if (panning?.pointer === event.pointerId) {
      view = panBy(view, event.clientX - panning.x, event.clientY - panning.y);
      panning = { ...panning, x: event.clientX, y: event.clientY };
      return;
    }
    if (!sketch || sketch.pointer !== event.pointerId) return;
    const at = pointOf(event);
    sketch = { ...sketch, points: [...sketch.points, [at.x, at.y]] };
  };
  const toolUp = (event: PointerEvent) => {
    if (linkLanded(event, true)) {
      ui.tool = "select";
      return;
    }
    if (panning?.pointer === event.pointerId) {
      panning = null;
      return;
    }
    if (!sketch || sketch.pointer !== event.pointerId) return;
    const { kind } = sketch;
    const shape = draft;
    sketch = null;
    ui.tool = "select";
    if (!shape) return;
    // A twitch is not a drawing: nothing under a few stage units is
    // kept, and a pencil stroke keeps only the points that moved.
    if (kind === "frame") {
      if (shape.w >= frameMin.w && shape.h >= frameMin.h) layout.addFrame(shape);
      return;
    }
    if (kind === "pencil") {
      const points = simplified(shape.points ?? []);
      if (points.length >= 2) {
        layout.addShape(strokeOf("pencil", points.map(([x, y]) => [x + shape.x, y + shape.y])));
      }
      return;
    }
    if (Math.max(shape.w, shape.h) < 4) return;
    layout.addShape(shape);
  };
  const toolCancel = (event: PointerEvent) => {
    if (linkLanded(event, false)) return;
    if (panning?.pointer === event.pointerId) panning = null;
    if (sketch?.pointer === event.pointerId) sketch = null;
  };

  /**
   * The pipeline on the canvas. A link is drawn by dragging an arrow out
   * of a tile's port — or, under the arrow tool, out of anywhere on a
   * tile — and dropping it on another tile. The server owns the link
   * from then on: it fires from the provider hook, and what this canvas
   * shows is what the roster push says. Drawing it is a request.
   */
  let linking = $state<{ pointer: number; from: string; to: { x: number; y: number } } | null>(null);
  let chosenLink = $state<string | null>(null);
  let editingLabel = $state("");
  let labelField = $state<HTMLInputElement>();

  const tileBoxes = () =>
    agents
      .filter((a) => layout.tiles[a.id] !== undefined)
      .map((a) => ({ id: a.id, box: shownBox(a.id) }));

  const startLink = (from: string, event: PointerEvent) => {
    if (event.button !== 0) return;
    chosen = null;
    chosenLink = null;
    linking = { pointer: event.pointerId, from, to: pointOf(event) };
    clip?.setPointerCapture?.(event.pointerId);
  };
  const linkMoved = (event: PointerEvent): boolean => {
    if (!linking || linking.pointer !== event.pointerId) return false;
    linking = { ...linking, to: pointOf(event) };
    return true;
  };
  const linkLanded = (event: PointerEvent, commit: boolean): boolean => {
    if (!linking || linking.pointer !== event.pointerId) return false;
    const { from, to } = linking;
    linking = null;
    if (!commit) return true;
    const target = boxAt(tileBoxes(), to);
    if (!target || target === from) return true;
    void act(async () => {
      const added = await api.addLink({ from, to: target, label: "", auto: true });
      // The push will carry it; choosing it now opens its label for
      // typing, which is what a freshly drawn arrow is waiting for.
      chosenLink = added.id;
      editingLabel = "";
    });
    return true;
  };

  const linkByID = (id: string | null) =>
    id ? fleet.links.find((link) => link.id === id) : undefined;
  const chosenOne = $derived(linkByID(chosenLink));
  // A chosen link that the push no longer carries — deleted elsewhere,
  // or its agent gone — is not chosen.
  $effect(() => {
    if (chosenLink && !fleet.links.some((link) => link.id === chosenLink)) {
      chosenLink = null;
    }
  });
  // The label takes the keyboard when a link is chosen — once, on the
  // choosing. Not on every push: the chosen link's object is replaced
  // with each roster, and an effect keyed on it would pull the focus
  // back into the field every second, out of whatever was typing.
  $effect(() => {
    const id = chosenLink;
    if (!id) return;
    void tick().then(() => labelField?.focus());
  });

  const pickLink = (id: string, event: PointerEvent) => {
    if (event.button !== 0) return;
    event.stopPropagation();
    event.preventDefault();
    chosen = null;
    chosenLink = id;
    editingLabel = linkByID(id)?.label ?? "";
    clip?.focus();
  };
  const saveLabel = () => {
    const link = chosenOne;
    if (!link) return;
    const label = editingLabel.trim();
    if (label === link.label) return;
    void act(() => api.updateLink(link.id, { label }));
  };
  // Enter and Escape both leave the field, and leaving is what saves —
  // one path, so a label is never sent twice for one keystroke.
  const labelKey = (event: KeyboardEvent) => {
    if (event.key === "Enter") {
      event.preventDefault();
      labelField?.blur();
    } else if (event.key === "Escape") {
      event.preventDefault();
      editingLabel = chosenOne?.label ?? "";
      labelField?.blur();
    }
  };
  const toggleAuto = () => {
    const link = chosenOne;
    if (link) void act(() => api.updateLink(link.id, { auto: !link.auto }));
  };
  const fire = () => {
    const link = chosenOne;
    if (link) void act(() => api.fireLink(link.id));
  };
  const removeLink = () => {
    const link = chosenOne;
    if (!link) return;
    chosenLink = null;
    void act(() => api.removeLink(link.id));
  };

  /**
   * What just happened, for the eye. A link whose last_fired moved
   * since the last push fired: its arrow pulses for a moment and the
   * target's label says who it heard from. A hop parked on a link
   * shows on the target too, as waiting. Kept as ids and timestamps
   * rather than derived from the push, since a pulse has to outlive
   * the tick that started it.
   */
  let fired = $state(new Set<string>());
  let seen = new Map<string, string>();
  let arrivals = $state<Record<string, string>>({});
  $effect(() => {
    const links = fleet.links;
    const names = new Map(fleet.agents.map((a) => [a.id, a.name || a.task || a.id.slice(0, 8)]));
    untrack(() => {
      for (const link of links) {
        const stamp = link.last_fired ?? "";
        const before = seen.get(link.id);
        seen.set(link.id, stamp);
        if (before === undefined || before === stamp || !stamp) continue;
        fired = new Set([...fired, link.id]);
        arrivals = { ...arrivals, [link.to]: `← from ${names.get(link.from) ?? link.from.slice(0, 8)}` };
        window.setTimeout(() => {
          fired = new Set([...fired].filter((id) => id !== link.id));
          const { [link.to]: _, ...rest } = arrivals;
          void _;
          arrivals = rest;
        }, 4000);
      }
    });
  });
  const inboundOf = (agentID: string): string => {
    if (arrivals[agentID]) return arrivals[agentID];
    const waiting = fleet.links.find((link) => link.to === agentID && link.pending);
    if (!waiting) return "";
    const from = fleet.agents.find((a) => a.id === waiting.from);
    return `⏳ ${from?.name || from?.task || waiting.from.slice(0, 8)} waiting`;
  };

  /**
   * The select tool over a drawing: a press chooses it and, if the hand
   * moves, carries it. Delete or Backspace on a chosen drawing removes
   * it; the canvas holds the focus for that, and holding it is why a
   * press on a drawing is not cancelled — the keyboard has to land
   * somewhere Delete can reach.
   */
  let chosen = $state<string | null>(null);
  let moving = $state<{ id: string; pointer: number; lastX: number; lastY: number; dx: number; dy: number } | null>(null);
  const pick = (id: string, event: PointerEvent) => {
    if (event.button !== 0) return;
    event.stopPropagation();
    event.preventDefault();
    chosen = id;
    clip?.focus();
    clip?.setPointerCapture?.(event.pointerId);
    moving = { id, pointer: event.pointerId, lastX: event.clientX, lastY: event.clientY, dx: 0, dy: 0 };
  };
  const shapeMoved = (event: PointerEvent): boolean => {
    if (!moving || moving.pointer !== event.pointerId) return false;
    moving = {
      ...moving,
      lastX: event.clientX,
      lastY: event.clientY,
      dx: moving.dx + (event.clientX - moving.lastX) / view.z,
      dy: moving.dy + (event.clientY - moving.lastY) / view.z,
    };
    return true;
  };
  const shapeLanded = (event: PointerEvent, commit: boolean): boolean => {
    if (!moving || moving.pointer !== event.pointerId) return false;
    const { id, dx, dy } = moving;
    moving = null;
    if (commit && (dx !== 0 || dy !== 0) && layout.shapes[id]) {
      const shape = layout.shapes[id];
      layout.putShape(id, { ...shape, x: shape.x + dx, y: shape.y + dy });
    }
    return true;
  };
  const canvasKey = (event: KeyboardEvent) => {
    if (event.target !== clip) return;
    if (event.key !== "Delete" && event.key !== "Backspace") return;
    if (chosen) {
      event.preventDefault();
      layout.dropShape(chosen);
      chosen = null;
    } else if (chosenLink) {
      event.preventDefault();
      removeLink();
    }
  };
  // Picking up a tool lets go of the chosen drawing; Escape, which
  // returns the select tool, has already dropped it by then.
  $effect(() => {
    if (ui.tool !== "select") chosen = null;
  });

  /** One frame per workspace around its tiles: the wall's grouping,
   *  made spatial, on the canvas that shows everyone. */
  const byWorkspace = () => {
    const groups = new Map<string, { name: string; boxes: Box[] }>();
    for (const agent of agents) {
      const box = layout.tiles[agent.id];
      const workspace = agent.workspace;
      if (!box || !workspace) continue;
      const group = groups.get(workspace.id) ?? { name: workspace.name, boxes: [] };
      group.boxes.push(box);
      groups.set(workspace.id, group);
    }
    for (const group of groups.values()) {
      layout.addFrame(frameAround(group.boxes), group.name);
    }
  };
  const workspacesShown = $derived(
    new Set(agents.map((a) => a.workspace?.id).filter(Boolean)).size,
  );

  // Placement is minted here, in an effect, never from the template:
  // boxFor writes state for an agent it has not seen, and Svelte
  // (rightly) refuses state mutation during render. The template below
  // renders only tiles that already have a box; this effect makes that
  // true one tick after any new agent appears.
  $effect(() => {
    for (const agent of agents) layout.boxFor(agent.id);
  });

  const boxes = (): Box[] =>
    agents
      .map((a) => layout.tiles[a.id])
      .filter((box): box is Box => box !== undefined);

  /** Frames as boxes, title bars included, for fitting. */
  const frameBoxes = (): Box[] =>
    Object.values(layout.frames).map((f) => ({
      x: f.x,
      y: f.y - frameTitle,
      w: f.w,
      h: f.h + frameTitle,
    }));

  /** The frame a tile sits in, and whether that frame holds it still. */
  const frameOfTile = (id: string) =>
    layout.tiles[id] ? frameOf(layout.frames, layout.tiles[id]) : undefined;
  const lockedTile = (id: string): boolean => {
    const frameID = frameOfTile(id);
    return frameID !== undefined && layout.frames[frameID].locked;
  };

  const fit = () => {
    // A viewport with no extent — hidden tab, mid-layout mount — has
    // nothing to fit into, and fitting anyway slams the zoom to its
    // floor.
    if (!clip || clip.clientWidth === 0 || clip.clientHeight === 0) return;
    view = fitView([...boxes(), ...frameBoxes()], {
      w: clip.clientWidth,
      h: clip.clientHeight,
    });
  };

  // Open on the whole fleet — once per workspace, not once per mount:
  // switching workspaces in the rail swaps the whole arrangement, and a
  // camera still aimed at the previous one shows empty space. Waiting
  // for the roster matters too: the canvas usually mounts before the
  // first push, and fitting zero boxes is just the home view.
  let fittedFor: string | null = null;
  $effect(() => {
    const workspace = fleet.workspaceID;
    if (fittedFor === workspace || agents.length === 0 || !clip) return;
    fittedFor = workspace;
    fit();
  });

  const viewport = () => ({ w: clip!.clientWidth, h: clip!.clientHeight });

  /** The camera, centred on a box at a zoom it can be read at. */
  const centerOn = (box: Box) => {
    if (!clip) return;
    view = centeredOn(view, box, viewport());
  };

  // The cursor can move by key — alt+j, alt+n — onto a tile the hand
  // never went near, and on a canvas that may be ten screens away. A
  // selection nobody can see is not one, so a tile wholly off screen is
  // brought to the centre; one that is even partly on screen is left
  // where the hand put the camera.
  //
  // Each selection is revealed once, when it has a tile — which may be
  // a tick after it was made, since a dispatched agent is selected
  // before its box is minted. The box is tracked for that tick and no
  // longer: a drag that rewrites the selected tile's box re-runs this,
  // and revealing it again would have the camera chase the tile the
  // hand just pushed off screen. The camera is never tracked, or a pan
  // that carried the tile off the edge would snap it back.
  let revealed = "";
  $effect(() => {
    const id = fleet.selectedID;
    const box = layout.tiles[id];
    if (!box || !clip || revealed === id) return;
    revealed = id;
    untrack(() => {
      if (!showing(view, box, viewport())) centerOn(box);
    });
  });

  /**
   * The wheel is the pan, and pinch is the zoom. Trackpads report a
   * pinch as a wheel event with ctrlKey set — the same convention
   * Figma, Excalidraw, and every other canvas rely on — so both
   * gestures arrive through one handler.
   */
  const wheel = (event: WheelEvent) => {
    event.preventDefault();
    if (event.ctrlKey || event.metaKey) {
      const bounds = clip!.getBoundingClientRect();
      view = zoomAt(
        view,
        { x: event.clientX - bounds.left, y: event.clientY - bounds.top },
        Math.exp(-event.deltaY * 0.01),
      );
      return;
    }
    view = panBy(view, -event.deltaX, -event.deltaY);
  };

  /**
   * Dragging empty canvas pans; with Shift held it draws a marquee, and
   * letting go selects every tile the marquee touches. With the frame
   * tool armed the same drag draws a frame instead. Tiles stop
   * propagation of their own gestures by handling them first (their
   * pointerdown captures).
   */
  let panning: { pointer: number; x: number; y: number } | null = null;
  let marquee = $state<{
    pointer: number;
    from: { x: number; y: number };
    to: { x: number; y: number };
  } | null>(null);
  const band = $derived(marquee ? spanning(marquee.from, marquee.to) : null);

  const pointOf = (event: PointerEvent) => {
    const bounds = clip!.getBoundingClientRect();
    return stagePoint(view, {
      x: event.clientX - bounds.left,
      y: event.clientY - bounds.top,
    });
  };

  const down = (event: PointerEvent) => {
    if (event.button !== 0) return;
    // Only the backdrop pans. A pointerdown that began on a tile is the
    // tile's gesture; it captures its pointer, so it never surfaces
    // here with the backdrop as target.
    if (event.target !== event.currentTarget && event.target !== stage) return;
    // Cancelled so the drag selects no text: a marquee across the
    // corner controls otherwise highlights their labels, and the
    // backdrop has nothing else a press could mean.
    event.preventDefault();
    chosen = null;
    chosenLink = null;
    clip?.setPointerCapture?.(event.pointerId);
    if (event.shiftKey) {
      const at = pointOf(event);
      marquee = { pointer: event.pointerId, from: at, to: at };
      return;
    }
    panning = { pointer: event.pointerId, x: event.clientX, y: event.clientY };
  };
  const moved = (event: PointerEvent) => {
    if (linkMoved(event)) return;
    if (shapeMoved(event)) return;
    if (marquee?.pointer === event.pointerId) {
      marquee = { ...marquee, to: pointOf(event) };
      return;
    }
    if (!panning || event.pointerId !== panning.pointer) return;
    view = panBy(view, event.clientX - panning.x, event.clientY - panning.y);
    panning = { ...panning, x: event.clientX, y: event.clientY };
  };
  const up = (event: PointerEvent) => {
    if (linkLanded(event, true)) return;
    if (shapeLanded(event, true)) return;
    if (panning?.pointer === event.pointerId) panning = null;
    if (marquee?.pointer === event.pointerId && band) {
      // Whatever the band touches, and nothing if it touched nothing:
      // a shift-drag over empty canvas is how a selection is dropped.
      select(
        agents
          .filter((agent) => {
            const box = layout.tiles[agent.id];
            return box !== undefined && intersects(band, box);
          })
          .map((agent) => agent.id),
      );
      marquee = null;
    }
  };
  const cancelled = (event: PointerEvent) => {
    if (linkLanded(event, false)) return;
    if (shapeLanded(event, false)) return;
    if (panning?.pointer === event.pointerId) panning = null;
    if (marquee?.pointer === event.pointerId) marquee = null;
  };

  let stage = $state<HTMLDivElement>();

  const zoomLabel = $derived(`${Math.round(view.z * 100)}%`);

  // The wall sorts urgent agents to the front; a canvas cannot — the
  // user owns placement, and an urgent tile may sit ten screens away.
  // This is the signal that survives that: a count that is always in
  // the corner, and a jump that centres each urgent tile in turn.
  const urgent = $derived(agents.filter(isUrgent));
  let jumpAt = 0;
  const jump = () => {
    if (urgent.length === 0 || !clip) return;
    // Walk from the cursor to the next urgent agent that has a tile;
    // burning an index on one minted a tick from now would silently
    // skip an agent per press.
    let box;
    for (let step = 0; step < urgent.length; step++) {
      const target = urgent[(jumpAt + step) % urgent.length];
      box = layout.tiles[target.id];
      if (box) {
        jumpAt = (jumpAt + step + 1) % urgent.length;
        break;
      }
    }
    if (!box) return;
    centerOn(box);
  };

  /** A click on a tile: the cursor moves there and the keyboard with
   *  it. Both are the roster's own commands, which on this view stay
   *  on this view. */
  const enter = (id: string) => {
    run("select-agent", id);
    run("walk-in");
  };

  /** A shift-click: the tile joins or leaves the selection, and the
   *  cursor moves to it. Building a selection is arranging, not typing,
   *  so the walk ends — otherwise the keyboard would follow the cursor
   *  onto each tile shift-clicked, and the tile it lands on would drag
   *  by its label alone. */
  const toggle = (id: string) => {
    if (ui.selection.has(id)) ui.selection.delete(id);
    else ui.selection.add(id);
    fleet.selectedID = id;
    ui.walkedIn = false;
  };

  /** The label's ↗: the roster's full pane, to look at. Letting go of
   *  the keyboard is said here rather than left to focus, because
   *  whether a button takes focus on click is the browser's opinion —
   *  and arriving on the roster typing to an agent nobody walked into
   *  is the wrong answer on any of them. */
  const open = (id: string) => {
    ui.walkedIn = false;
    run("select-agent", id);
    onopen();
  };
</script>

<!-- role=application: the surface really is one — every pointer and
     wheel event is a camera or tile gesture, not document scrolling —
     and it tells assistive tech the tiles inside carry the semantics. -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="canvas"
  role="application"
  aria-label="Agent canvas"
  tabindex="-1"
  bind:this={clip}
  onwheel={wheel}
  onpointerdown={down}
  onpointermove={moved}
  onpointerup={up}
  onpointercancel={cancelled}
  onkeydown={canvasKey}
>
  <div
    class="stage"
    bind:this={stage}
    style:transform="translate({view.x}px, {view.y}px) scale({view.z})"
  >
    <!-- Frames first, so tiles paint over them without a z-index. -->
    {#each Object.keys(layout.frames) as id (id)}
      <CanvasFrame
        frame={layout.frames[id]}
        box={shownFrame(id)}
        zoom={view.z}
        lifted={drift?.kind === "frame" && drift.id === id}
        oncommit={(box) => layout.putFrame(id, { ...layout.frames[id], ...box })}
        ondrift={(dx, dy) => drifted("frame", id, dx, dy)}
        onland={landed}
        onrename={(name) => layout.putFrame(id, { ...layout.frames[id], name })}
        onlock={(locked) => layout.putFrame(id, { ...layout.frames[id], locked })}
        ondelete={() => layout.dropFrame(id)}
      />
    {/each}
    {#each agents as agent (agent.id)}
      {#if layout.tiles[agent.id]}
        <CanvasTile
          {agent}
          box={shownBox(agent.id)}
          zoom={view.z}
          {clip}
          cursor={fleet.selectedID === agent.id}
          selected={ui.selection.has(agent.id)}
          lifted={carried(agent.id)}
          locked={lockedTile(agent.id)}
          focused={focused === agent.id}
          inbound={inboundOf(agent.id)}
          oncommit={(box) => layout.put(agent.id, box)}
          onlink={(event) => startLink(agent.id, event)}
          ondrift={(dx, dy) => drifted("tile", agent.id, dx, dy)}
          onland={landed}
          onenter={() => enter(agent.id)}
          ontoggle={() => toggle(agent.id)}
          onopen={() => open(agent.id)}
        />
      {/if}
    {/each}
    <!-- Drawings over everything: an arrow across a terminal is meant
         to be seen across it. -->
    <CanvasDrawings
      shapes={layout.shapes}
      {draft}
      {chosen}
      moving={moving && { id: moving.id, dx: moving.dx, dy: moving.dy }}
      zoom={view.z}
      interactive={ui.tool === "select"}
      onpick={pick}
    />
    <!-- The pipeline, over the tiles: an arrow across a terminal is
         meant to be seen across it. -->
    <CanvasLinks
      links={fleet.links}
      boxOf={(id) => (layout.tiles[id] && agents.some((a) => a.id === id) ? shownBox(id) : undefined)}
      draft={linking}
      chosen={chosenLink}
      {fired}
      zoom={view.z}
      interactive={ui.tool === "select"}
      onpick={pickLink}
    />
    {#if chosenOne}
      {@const from = layout.tiles[chosenOne.from]}
      {@const to = layout.tiles[chosenOne.to]}
      {#if from && to}
        {@const at = arrowBetween(shownBox(chosenOne.from), shownBox(chosenOne.to)).mid}
        <!-- The chosen link's controls: the label is the prompt, so it
             is the first thing; auto, fire and delete beside it. Drawn
             in stage units at the arrow's middle and counter-scaled,
             so it reads the same at any zoom. -->
        <div
          class="pill"
          role="group"
          aria-label="Link"
          style:left="{at.x}px"
          style:top="{at.y}px"
          style:transform="translate(-50%, {14 / view.z}px) scale({1 / view.z})"
          onpointerdown={(event) => event.stopPropagation()}
        >
          <input
            bind:this={labelField}
            bind:value={editingLabel}
            placeholder="what the next agent should do with this"
            aria-label="Link label"
            spellcheck="false"
            onkeydown={labelKey}
            onblur={saveLabel}
          />
          <button
            class:on={chosenOne.auto}
            title={chosenOne.auto ? "Fires when the source's turn ends" : "Fired by hand only"}
            aria-label={chosenOne.auto ? "Auto: on" : "Auto: off"}
            onclick={toggleAuto}
          >
            ⟳
          </button>
          <button title="Fire now" aria-label="Fire the link now" onclick={fire}>▶</button>
          <button class="danger" title="Delete the link" aria-label="Delete the link" onclick={removeLink}>✕</button>
        </div>
      {/if}
    {/if}
    {#if band}
      <div
        class="marquee"
        style:left="{band.x}px"
        style:top="{band.y}px"
        style:width="{band.w}px"
        style:height="{band.h}px"
      ></div>
    {/if}
    {#if composing}
      <!-- svelte-ignore a11y_autofocus -->
      <textarea
        class="composer"
        bind:this={composer}
        value={composing.text}
        oninput={(event) => {
          if (composing) composing.text = event.currentTarget.value;
        }}
        style:left="{composing.x}px"
        style:top="{composing.y}px"
        aria-label="Text"
        rows="1"
        spellcheck="false"
        onkeydown={composerKey}
        onblur={commitText}
        onpointerdown={(event) => event.stopPropagation()}
      ></textarea>
    {/if}
  </div>
  {#if ui.tool !== "select"}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="overlay {ui.tool}"
      onpointerdown={toolDown}
      onpointermove={toolMove}
      onpointerup={toolUp}
      onpointercancel={toolCancel}
    ></div>
  {/if}
  <CanvasTools />
  {#if agents.length === 0}
    <p class="empty">No agents to arrange.</p>
  {/if}
  <div class="controls">
    {#if urgent.length > 0}
      <button class="urgent" onclick={jump} title="Jump to the next agent that needs input">
        ! {urgent.length} need{urgent.length === 1 ? "s" : ""} input
      </button>
    {/if}
    {#if fleet.workspaceID === "" && workspacesShown > 1}
      <button onclick={byWorkspace} title="Draw one frame around each workspace's tiles">
        frame by workspace
      </button>
    {/if}
    <button onclick={fit} title="Fit every tile in view">fit</button>
    <span class="zoom">{zoomLabel}</span>
  </div>
</div>

<style>
  .canvas {
    position: relative;
    flex: 1 1 auto;
    overflow: hidden;
    /* The canvas owns its gestures; the page must not scroll or
       pinch-zoom underneath them. */
    touch-action: none;
    background:
      radial-gradient(circle, var(--border) 1px, transparent 1px) 0 0 / 28px
        28px,
      var(--bg);
    cursor: grab;
  }
  .canvas:active {
    cursor: grabbing;
  }
  .canvas:focus {
    outline: none;
  }
  /* Under a tool, the overlay has the pointer: every press is the
     tool's, wherever it lands. */
  .overlay {
    position: absolute;
    inset: 0;
    cursor: crosshair;
  }
  .overlay.hand {
    cursor: grab;
  }
  .overlay.hand:active {
    cursor: grabbing;
  }
  .overlay.text {
    cursor: text;
  }
  .pill {
    position: absolute;
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 4px 6px;
    background: var(--bg-raised);
    border: 1px solid var(--accent);
    border-radius: 6px;
    box-shadow: 0 4px 16px var(--shadow-lift);
    transform-origin: top center;
    white-space: nowrap;
  }
  .pill input {
    width: 32ch;
    padding: 2px 6px;
    background: var(--field);
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--text-bright);
    font: 12px "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  }
  .pill input:focus {
    outline: none;
    border-color: var(--accent);
  }
  .pill button {
    padding: 2px 7px;
    border: 1px solid transparent;
    border-radius: 4px;
    background: transparent;
    color: var(--muted);
    font-size: 12px;
    cursor: pointer;
  }
  .pill button:hover {
    color: var(--accent);
  }
  .pill button.on {
    background: var(--band);
    color: var(--band-ink);
  }
  .pill button.danger:hover {
    color: var(--failed);
  }
  .composer {
    position: absolute;
    min-width: 12ch;
    margin: 0;
    padding: 0;
    background: transparent;
    border: 1px dashed var(--accent);
    color: var(--text-bright);
    font: 14px "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
    line-height: 1.3;
    resize: none;
    overflow: hidden;
  }
  .composer:focus {
    outline: none;
  }
  .stage {
    position: absolute;
    top: 0;
    left: 0;
    /* The stage is a point, not a box: tiles position themselves on it
       in stage units, and the transform above is the whole camera. */
    width: 0;
    height: 0;
    transform-origin: top left;
  }
  .marquee {
    position: absolute;
    /* Drawn in stage units like a tile, so it pans and zooms with the
       tiles it is choosing among. */
    border: 1px dashed var(--accent);
    background: var(--selected-bg);
    pointer-events: none;
  }
  .empty {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    margin: 0;
    color: var(--muted);
    font-size: 12px;
    pointer-events: none;
  }
  .controls {
    position: absolute;
    right: 12px;
    bottom: 12px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 6px;
    background: var(--bg-raised);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .controls button {
    padding: 1px 10px;
    border: none;
    background: transparent;
    color: var(--muted);
    font-size: 12px;
    cursor: pointer;
  }
  .controls button:hover {
    color: var(--accent);
  }
  .controls button.urgent {
    color: var(--waiting);
    font-weight: 600;
  }
  .zoom {
    min-width: 4ch;
    color: var(--muted);
    font-size: 11px;
    text-align: right;
  }
</style>
