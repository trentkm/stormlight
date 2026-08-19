// @vitest-environment jsdom
//
// The terminal's focus, which is the whole walked-in contract seen from
// the other side.
//
// xterm focuses itself on mousedown from its own listener, so a click on
// a terminal nobody walked into left it holding the keyboard — and every
// key the page does not claim went down the socket to a live agent.
// Reacting to the `focused` prop cannot see that, because the prop did
// not change; only watching focus can.
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import { reactive } from "./testing.svelte";

// vi.hoisted, because vi.mock's factory is lifted above every ordinary
// top-level binding and would otherwise reach a class that does not
// exist yet.
const { FakeTerminal } = vi.hoisted(() => {
  /** A terminal that records focus and blur the way xterm does —
   *  including xterm's own habit of focusing itself when clicked, which
   *  is the behaviour this whole file exists to pin. */
  class FakeTerminal {
    static live: FakeTerminal[] = [];
    helper!: HTMLTextAreaElement;
    host!: HTMLElement;
    focuses = 0;
    blurs = 0;
    scrolled = 0;
    /** Which buffer the agent is drawing in — the thing that decides
     *  whether the wheel is ours or the program's. */
    bufferType: "normal" | "alternate" = "normal";
    wheel?: (event: WheelEvent) => boolean;
    get buffer() {
      return { active: { type: this.bufferType } };
    }
    attachCustomWheelEventHandler(handler: (event: WheelEvent) => boolean) {
      this.wheel = handler;
    }
    scrollLines(lines: number) {
      this.scrolled += lines;
    }
    bottoms = 0;
    scrollToBottom() {
      this.bottoms++;
    }

    constructor() {
      FakeTerminal.live.push(this);
    }
    open(host: HTMLElement) {
      this.host = host;
      this.helper = document.createElement("textarea");
      this.helper.className = "xterm-helper-textarea";
      host.append(this.helper);
      host.addEventListener("mousedown", () => this.focus());
    }
    focus() {
      this.focuses++;
      this.helper.focus();
    }
    blur() {
      this.blurs++;
      this.helper.blur();
    }
    loadAddon() {}
    dispose() {
      FakeTerminal.live = FakeTerminal.live.filter((term) => term !== this);
    }
  }
  return { FakeTerminal };
});

vi.mock("@xterm/xterm", () => ({ Terminal: FakeTerminal }));
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
  },
}));
vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: class {},
}));
vi.mock("../lib/terminal", () => ({
  attach: () => ({ fit: () => {}, close: () => {} }),
}));

vi.stubGlobal(
  "ResizeObserver",
  class {
    observe() {}
    disconnect() {}
  },
);

import Terminal from "./Terminal.svelte";

const leaked: Array<() => void> = [];
afterEach(() => {
  while (leaked.length) leaked.pop()!();
});

const entered: number[] = [];

function show(props: { agentID: string; focused: boolean }) {
  const reactiveProps = reactive({
    ...props,
    onenter: () => entered.push(1),
  });
  const target = document.createElement("div");
  document.body.append(target);
  const term = mount(Terminal, { target, props: reactiveProps });
  flushSync();
  let done = false;
  const close = () => {
    if (done) return;
    done = true;
    unmount(term);
    target.remove();
  };
  leaked.push(close);
  return { props: reactiveProps, close };
}

function click() {
  const host = document.querySelector(".terminal")!;
  host.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  flushSync();
}

function focusedElement() {
  const active = document.activeElement;
  return {
    inTerminal: !!(active instanceof HTMLElement && active.closest(".terminal")),
    tag: active?.tagName,
  };
}

beforeEach(() => {
  FakeTerminal.live = [];
  entered.length = 0;
});

/**
 * The wheel. An agent that asked for mouse reporting receives wheel
 * events as input, and a CLI with a prompt reads them as history — so
 * scrolling up over the transcript walked through past prompts instead
 * of showing what had scrolled off.
 */
describe("whose wheel it is", () => {
  function turn(deltaY: number) {
    const term = FakeTerminal.live[0];
    return term.wheel?.(new WheelEvent("wheel", { deltaY }));
  }

  test("scrolls the terminal rather than reaching the agent", () => {
    const { close } = show({ agentID: "a1", focused: true });
    const term = FakeTerminal.live[0];

    const handled = turn(120);

    expect(term.scrolled).toBeGreaterThan(0);
    // false: xterm must not also send it onward as input.
    expect(handled).toBe(false);
    close();
  });

  test("scrolls back up as well as down", () => {
    const { close } = show({ agentID: "a1", focused: true });
    const term = FakeTerminal.live[0];

    turn(-120);

    expect(term.scrolled).toBeLessThan(0);
    close();
  });

  // A full-screen program has no scrollback, and the wheel is how it
  // is driven — that one belongs to the agent.
  test("leaves the wheel to a full-screen program", () => {
    const { close } = show({ agentID: "a1", focused: true });
    const term = FakeTerminal.live[0];
    term.bufferType = "alternate";

    const handled = turn(120);

    expect(term.scrolled).toBe(0);
    expect(handled).toBe(true);
    close();
  });
});

describe("who holds the keyboard", () => {
  // The focus rules find the walked-in terminal by this hook. Every
  // other test in the suite supplies its own markup, so without this
  // one the attribute could be deleted from the shipping component
  // with all 169 tests green — and every walk would end on the first
  // focusout, because the query would find nothing.
  test("the terminal carries the hook the walk is found by", () => {
    const { close } = show({ agentID: "a1", focused: false });

    const hook = document.querySelector("[data-walk-target]");
    expect(hook).not.toBeNull();
    expect(hook!.querySelector("textarea")).not.toBeNull();
    close();
  });


  // A click on a terminal means "I want to type here" — so it asks.
  test("a click asks to walk in", () => {
    const { close } = show({ agentID: "a1", focused: false });

    click();

    expect(entered.length).toBe(1);
    close();
  });

  test("a click the page does not grant leaves the keyboard alone", async () => {
    // The page says no by leaving `focused` false. This is the failure
    // that once reached a live agent: xterm focuses itself on
    // mousedown, and if nothing takes it back, the next keystroke is
    // typed into someone's session.
    const { close } = show({ agentID: "a1", focused: false });

    click();
    flushSync();
    expect(focusedElement().inTerminal).toBe(false);

    // And the claim does not outlive the click that made it: a focus
    // arriving later — a stray click, a programmatic focus — is not
    // covered by a request the page already refused.
    await Promise.resolve();
    FakeTerminal.live[0].focus();
    document
      .querySelector(".terminal")!
      .dispatchEvent(new FocusEvent("focusin", { bubbles: true }));

    expect(focusedElement().inTerminal).toBe(false);
    close();
  });

  test("a click the page grants keeps it", () => {
    const { props, close } = show({ agentID: "a1", focused: false });

    click();
    // What Spanreed does with the request.
    props.focused = true;
    flushSync();

    expect(focusedElement().inTerminal).toBe(true);
    close();
  });

  test("a walked-in terminal keeps the focus a click gives it", () => {
    const { close } = show({ agentID: "a1", focused: true });

    click();

    expect(focusedElement().inTerminal).toBe(true);
    close();
  });

  // Arriving at a prompt that is scrolled off screen is arriving
  // nowhere: you are typing, and cannot see where.
  test("walking in follows the tail", () => {
    const { props, close } = show({ agentID: "a1", focused: false });
    const term = FakeTerminal.live[0];
    expect(term.bottoms).toBe(0);

    props.focused = true;
    flushSync();

    expect(term.bottoms).toBeGreaterThan(0);
    close();
  });

  test("walking in takes focus, and walking out gives it back", () => {
    const { props, close } = show({ agentID: "a1", focused: false });
    const term = FakeTerminal.live[0];

    props.focused = true;
    flushSync();
    expect(focusedElement().inTerminal).toBe(true);

    props.focused = false;
    flushSync();
    expect(focusedElement().inTerminal).toBe(false);
    expect(term.blurs).toBeGreaterThan(0);
    close();
  });
});
