/**
 * A canvas jsdom does not have.
 *
 * jsdom implements no 2D context, and xterm's DOM renderer builds a glyph
 * width cache over one the moment a terminal opens — so mounting any
 * component that renders a terminal throws before the test reaches what
 * it came to check. Measuring is the only thing asked of it here, and
 * monospace text is one advance per character.
 *
 * Loaded by the jsdom tests that mount a real terminal; the node tests
 * never see it, which keeps them honest about browser globals.
 */
const advance = 8;

export function stubCanvas(): void {
  if (typeof HTMLCanvasElement === "undefined") return;
  HTMLCanvasElement.prototype.getContext = function getContext(
    this: HTMLCanvasElement,
    kind: string,
  ) {
    if (kind !== "2d") return null;
    return {
      canvas: this,
      font: "",
      fillStyle: "",
      textBaseline: "",
      measureText: (text: string) => ({ width: text.length * advance }),
      fillText: () => {},
      clearRect: () => {},
      fillRect: () => {},
      getImageData: (_x: number, _y: number, w: number, h: number) => ({
        data: new Uint8ClampedArray(Math.max(1, w * h * 4)),
        width: w,
        height: h,
      }),
      putImageData: () => {},
      drawImage: () => {},
      save: () => {},
      restore: () => {},
      scale: () => {},
      translate: () => {},
      setTransform: () => {},
      createLinearGradient: () => ({ addColorStop: () => {} }),
    } as unknown as CanvasRenderingContext2D;
  } as typeof HTMLCanvasElement.prototype.getContext;
}
