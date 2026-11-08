import MarkdownIt from "markdown-it";
import { relativeFileURL } from "./files.js";

const markdown = new MarkdownIt({ html: false, linkify: true });

for (const [rule, attribute, preview] of [
  ["image", "src", true],
  ["link_open", "href", false],
]) {
  const original = markdown.renderer.rules[rule];
  markdown.renderer.rules[rule] = (tokens, index, options, env, self) => {
    const token = tokens[index];
    token.attrSet(
      attribute,
      relativeFileURL(env?.recordID, token.attrGet(attribute), preview),
    );
    return original
      ? original(tokens, index, options, env, self)
      : self.renderToken(tokens, index, options);
  };
}
export function storyUI(request) {
  let generation = 0,
    controller,
    decoder,
    retryTimer;
  const finishPart = (parts) => {
    if (decoder && parts.length) parts.at(-1).text += decoder.decode();
    decoder = null;
  };
  return {
    storyParts: [],
    storyNext: 0,
    storyBusy: false,
    storyComplete: false,
    storyError: "",
    storyDiagnostics: [],
    storyUnconfirmed: false,
    readingStory() {
      return this.inspecting && this.selected?.kind === "stories";
    },
    storyHTML(text, id) {
      return markdown.render(text, { recordID: id });
    },
    resetStory() {
      generation++;
      clearTimeout(retryTimer);
      controller?.abort();
      controller = decoder = null;
      this.storyParts = [];
      this.storyNext = 0;
      this.storyBusy = this.storyComplete = this.storyUnconfirmed = false;
      this.storyError = "";
      this.storyDiagnostics = [];
    },
    async loadStory() {
      if (!this.readingStory() || this.storyBusy || this.storyComplete) return;
      clearTimeout(retryTimer);
      const ticket = ++generation;
      const id = this.selected.id;
      controller = new AbortController();
      this.storyBusy = true;
      this.storyError = "";
      try {
        do {
          const page = await request(
            "/api/stories/" +
              encodeURIComponent(id) +
              "/compose?offset=" +
              this.storyNext,
            { signal: controller.signal },
          );
          if (ticket !== generation) return;
          if (page.truncated && !(page.next > this.storyNext))
            throw Error("The next story page could not be read.");
          const parts = (page.parts || []).map((part) => ({
            ...part,
            bytes: part.base64
              ? Uint8Array.from(atob(part.base64), (char) => char.charCodeAt(0))
              : new TextEncoder().encode(part.text || ""),
          }));
          for (const part of parts) {
            let last = this.storyParts.at(-1);
            if (!last || last.id !== part.id || last.start !== part.start) {
              finishPart(this.storyParts);
              decoder = new TextDecoder();
              last = {
                id: part.id,
                kind: part.kind,
                title: part.title,
                start: part.start,
                text: "",
              };
              this.storyParts.push(last);
              // Keep subsequent changes on Alpine's reactive object.
              last = this.storyParts.at(-1);
            }
            last.text += decoder.decode(part.bytes, { stream: true });
          }
          const messages = (page.diagnostics || []).map((d) =>
            [d.path, d.message].filter(Boolean).join(": "),
          );
          if (parts.some((part) => part.base64))
            messages.push(
              "Some saved bytes are not valid text. Replacement characters are shown; the original records are unchanged.",
            );
          this.storyDiagnostics = [
            ...new Set([...this.storyDiagnostics, ...messages]),
          ];
          this.storyUnconfirmed ||= !!page.unconfirmed;
          this.storyNext = page.next || 0;
          this.storyComplete = !page.truncated;
        } while (!this.storyComplete);
        finishPart(this.storyParts);
      } catch (e) {
        if (ticket === generation) {
          this.storyError = e.message;
          if (!e.status || e.status >= 500)
            retryTimer = setTimeout(() => this.loadStory(), 1000);
        }
      } finally {
        if (ticket === generation) this.storyBusy = false;
      }
    },
  };
}
