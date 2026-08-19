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

function show(props: { agentID: string; focused: boolean }) {
  const reactiveProps = reactive(props);
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
});

describe("who holds the keyboard", () => {
  test("a terminal nobody walked into does not keep focus when clicked", () => {
    const { close } = show({ agentID: "a1", focused: false });

    click();

    // This is the failure that reached a live agent: the click focuses
    // xterm, and if nothing takes it back, the next keystroke is typed
    // into someone's session.
    expect(focusedElement().inTerminal).toBe(false);
    close();
  });

  test("a walked-in terminal keeps the focus a click gives it", () => {
    const { close } = show({ agentID: "a1", focused: true });

    click();

    expect(focusedElement().inTerminal).toBe(true);
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
