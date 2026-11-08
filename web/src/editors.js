import { relativeFileURL } from "./files.js";
import { EditorView as CodeView, basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { markdown } from "@codemirror/lang-markdown";
import { yamlFrontmatter } from "@codemirror/lang-yaml";
import { EditorState as RichState } from "prosemirror-state";
import { EditorView as RichView } from "prosemirror-view";
import {
  defaultMarkdownParser,
  defaultMarkdownSerializer,
  MarkdownParser,
  MarkdownSerializer,
} from "prosemirror-markdown";
import { Schema } from "prosemirror-model";
import MarkdownIt from "markdown-it";

// CSS variables follow the app's explicit or device theme without rebuilding
// the editor, changing its document, or losing its selection and undo history.
const sourceHighlighting = HighlightStyle.define([
  { tag: [tags.name, tags.propertyName], color: "var(--code-key)" },
  { tag: [tags.string, tags.inserted], color: "var(--code-string)" },
  { tag: tags.number, color: "var(--code-number)" },
  {
    tag: [tags.keyword, tags.atom, tags.bool, tags.null],
    color: "var(--code-keyword)",
  },
  { tag: [tags.comment, tags.meta], color: "var(--code-comment)" },
  { tag: tags.heading, color: "var(--code-markup)", fontWeight: "700" },
  {
    tag: [tags.link, tags.url],
    color: "var(--code-key)",
    textDecoration: "underline",
  },
  { tag: tags.emphasis, fontStyle: "italic" },
  { tag: tags.strong, fontWeight: "700" },
  { tag: [tags.invalid, tags.deleted], color: "var(--error)" },
]);
const rawSpec = (inline) => ({
  inline,
  group: inline ? "inline" : "block",
  atom: true,
  attrs: { text: {} },
  toDOM: (node) => [
    inline ? "code" : "pre",
    { class: "raw-markdown", title: "Original HTML; edit in Source" },
    node.attrs.text,
  ],
});
const schema = new Schema({
  nodes: defaultMarkdownParser.schema.spec.nodes
    .addToEnd("raw_inline", rawSpec(true))
    .addToEnd("raw_block", rawSpec(false)),
  marks: defaultMarkdownParser.schema.spec.marks,
});
const parser = new MarkdownParser(
  schema,
  new MarkdownIt("commonmark", { html: true }),
  {
    ...defaultMarkdownParser.tokens,
    html_inline: {
      node: "raw_inline",
      getAttrs: (token) => ({ text: token.content }),
    },
    html_block: {
      node: "raw_block",
      getAttrs: (token) => ({ text: token.content }),
    },
  },
);
const serializer = new MarkdownSerializer(
  {
    ...defaultMarkdownSerializer.nodes,
    raw_inline(state, node) {
      state.write(node.attrs.text);
    },
    raw_block(state, node) {
      state.write(node.attrs.text);
      state.closeBlock(node);
    },
  },
  defaultMarkdownSerializer.marks,
);
import { exampleSetup } from "prosemirror-example-setup";

// Only the automatic header lines are protected; persistence enforces the rule too.
export function timestamps(source) {
  const header =
    source.match(/^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/)?.[1] || "";
  return header
    .split("\n")
    .filter((line) => /^(created_at|updated_at):/.test(line))
    .join("\n");
}
export function codeEditor(
  host,
  source,
  onChange,
  protectedFields = [],
  readOnly = false,
) {
  return new CodeView({
    parent: host,
    state: EditorState.create({
      doc: source,
      extensions: [
        basicSetup,
        syntaxHighlighting(sourceHighlighting),
        EditorState.readOnly.of(readOnly),
        CodeView.editable.of(!readOnly),
        yamlFrontmatter({ content: markdown() }),
        CodeView.lineWrapping,
        CodeView.contentAttributes.of({ "aria-label": "Markdown source" }),
        EditorState.transactionFilter.of((tr) => {
          if (!tr.docChanged) return tr;
          const changed = tr.newDoc.toString();
          const header =
            changed.match(/^\ufeff?---\r?\n[\s\S]*?\r?\n---(?:\r?\n|$)/)?.[0] ||
            "";
          if (
            protectedFields.some(
              (field) =>
                field &&
                !header
                  .replaceAll("\r\n", "\n")
                  .includes(field.replaceAll("\r\n", "\n")),
            )
          )
            return [];
          return timestamps(tr.startState.doc.toString()) ===
            timestamps(changed)
            ? tr
            : [];
        }),
        CodeView.updateListener.of((update) => {
          if (update.docChanged) onChange(update.state.doc.toString());
        }),
      ],
    }),
  });
}
export function richEditor(
  host,
  body,
  onChange,
  readOnly = false,
  recordID = "",
) {
  const doc = parser.parse(body);
  return new RichView(host, {
    nodeViews: {
      image(node) {
        const dom = document.createElement("img");
        dom.src = relativeFileURL(recordID, node.attrs.src, true);
        dom.alt = node.attrs.alt || "";
        if (node.attrs.title) dom.title = node.attrs.title;
        return { dom };
      },
    },
    markViews: {
      link(mark) {
        const dom = document.createElement("a");
        dom.href = relativeFileURL(recordID, mark.attrs.href);
        if (mark.attrs.title) dom.title = mark.attrs.title;
        return { dom, contentDOM: dom };
      },
    },
    editable: () => !readOnly,
    attributes: {
      "aria-label": "Record text",
      role: "textbox",
      "aria-multiline": "true",
    },
    state: RichState.create({
      doc,
      plugins: readOnly ? [] : exampleSetup({ schema }),
    }),
    dispatchTransaction(transaction) {
      const next = this.state.apply(transaction);
      this.updateState(next);
      if (transaction.docChanged) onChange(serializer.serialize(next.doc));
    },
  });
}
