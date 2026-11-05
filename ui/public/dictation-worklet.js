// The AudioWorklet of dictation (src/lib/dictation.ts): it passes the microphone's samples
// (channel 0, at the context's rate) to the page in batches of 4096 frames, and the rest when the
// page sends "flush". The page turns them into a 16 kHz mono WAV for the daemon.
class TmxDictation extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buf = new Float32Array(4096);
    this.n = 0;
    this.port.onmessage = (e) => {
      if (e.data === "flush") {
        this.port.postMessage(this.buf.slice(0, this.n));
        this.n = 0;
        this.port.postMessage("flushed");
      }
    };
  }

  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (ch) {
      for (let i = 0; i < ch.length; i++) {
        this.buf[this.n++] = ch[i];
        if (this.n === this.buf.length) {
          this.port.postMessage(this.buf.slice());
          this.n = 0;
        }
      }
    }
    return true;
  }
}

registerProcessor("tmx-dictation", TmxDictation);
