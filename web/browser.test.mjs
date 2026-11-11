import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  readdirSync,
  writeFileSync as rawWriteFileSync,
  mkdirSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve, dirname } from "node:path";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";
import { chromium } from "playwright";
import { arrangeTimeline } from "./src/timeline.js";
import { normalizeDate, localDate, localDateTime } from "./src/dates.js";
function writeFileSync(path, ...args) {
  mkdirSync(dirname(path), { recursive: true });
  return rawWriteFileSync(path, ...args);
}
let root, processHandle, url, browser, embeddingMock;
const errors = [];

test("ISO dates normalize equivalent inputs without accepting impossible calendar dates", () => {
  assert.equal(normalizeDate("19810501"), "1981-05-01");
  assert.equal(normalizeDate("1981-5-1"), "1981-05-01");
  assert.equal(normalizeDate("2024-02-29"), "2024-02-29");
  assert.equal(normalizeDate("1900-02-29"), null);
  assert.equal(normalizeDate("2026-04-31"), null);
  assert.equal(normalizeDate("07/13/1983"), null);
  assert.equal(normalizeDate("0099-02-03"), "0099-02-03");
  assert.equal(normalizeDate(""), "");
  assert.equal(localDate("1981-05-01"), "1981-05-01");
  assert.equal(localDate(null), "Unknown");
  assert.equal(localDateTime(undefined), "Unknown");
});

test("timeline placement preserves dates, respects partial order, and retains unplaced accounts", () => {
  const record = (id, title, time = {}) => ({
    id,
    title,
    time,
    issues: [],
    relations: [],
  });
  const input = [
    record("a", "Mary's walk", { date: "2001-01-01" }),
    record("b", "John's later visit"),
    record("c", "A broad period", {
      earliest: "1990-01-01",
      latest: "2010-12-31",
    }),
    record("d", "A memory without timing"),
    record("e", "An independent first event"),
    record("f", "An independent next event"),
  ];
  const before = JSON.stringify(input);
  const result = arrangeTimeline(input, [
    { before: "a", after: "b", usable: true },
    { before: "b", after: "c", usable: true },
    { before: "e", after: "f", usable: true },
  ]);
  const ids = result.map((entry) => entry.id);
  assert.ok(
    ids.indexOf("a") < ids.indexOf("b") && ids.indexOf("b") < ids.indexOf("c"),
    "Links take precedence over overlapping range starts",
  );
  assert.equal(
    result.find((entry) => entry.id === "b").dateLabel,
    "Date unknown",
  );
  assert.equal(result.find((entry) => entry.id === "b").section, "dated");
  assert.equal(result.find((entry) => entry.id === "d").section, "undated");
  assert.equal(result.find((entry) => entry.id === "e").section, "relative");
  assert.ok(ids.indexOf("e") < ids.indexOf("f"));
  assert.equal(
    JSON.stringify(input),
    before,
    "Presentation must not alter stored dates or links",
  );
  const conflicted = arrangeTimeline(input, [
    { before: "a", after: "b", usable: false },
  ]);
  assert.equal(conflicted.find((entry) => entry.id === "a").timing, "conflict");
  assert.equal(
    conflicted.find((entry) => entry.id === "a").section,
    "dated",
    "A conflict must not hide a known calendar position",
  );
  assert.equal(conflicted.find((entry) => entry.id === "b").section, "review");
});
before(
  async () => {
    root = mkdtempSync(join(tmpdir(), "songya-ui-browser-"));
    execFileSync(
      "go",
      ["build", "-o", join(root, "songya-web"), "./cmd/songya-web"],
      { cwd: resolve("..") },
    );
    processHandle = spawn(
      join(root, "songya-web"),
      ["--store", join(root, "store"), "--listen", "127.0.0.1:0"],
      { cwd: root },
    );
    let output = "";
    url = await new Promise((done, reject) => {
      processHandle.stdout.on("data", (chunk) => {
        output += chunk;
        const match = output.match(/http:\/\/[^\s]+/);
        if (match) done(match[0]);
      });
      processHandle.on("error", reject);
      processHandle.on("exit", (code) =>
        reject(Error("Server exited " + code)),
      );
    });
    embeddingMock = createServer(async (req, res) => {
      let raw = "";
      for await (const chunk of req) raw += chunk;
      const body = JSON.parse(raw);
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          data: [
            {
              embedding: String(body.input).includes("needle-semantic")
                ? [1, 0]
                : [0, 1],
            },
          ],
        }),
      );
    });
    embeddingMock.listen(0, "127.0.0.1");
    await once(embeddingMock, "listening");
    writeFileSync(
      join(root, "store", "config.json"),
      JSON.stringify({
        embeddings: {
          base_url: `http://127.0.0.1:${embeddingMock.address().port}`,
          model: "fixture",
        },
      }),
    );
    browser = await chromium.launch({
      headless: true,
      args: ["--host-resolver-rules=MAP songya.test 127.0.0.1"],
    });
  },
  { timeout: 120000 },
);
after(async () => {
  await browser?.close();
  if (processHandle?.exitCode === null) {
    processHandle.kill("SIGTERM");
    await once(processHandle, "exit");
  }
  embeddingMock?.closeAllConnections();
  embeddingMock?.close();
  if (root) rmSync(root, { recursive: true, force: true });
});
async function pageFor(
  viewport = { width: 1320, height: 900 },
  init,
  view = "list",
  origin = url,
  options = {},
) {
  const context = await browser.newContext({ viewport, ...options });
  if (init) await context.addInitScript(init);
  const requests = [];
  context.on("request", (request) => requests.push(request.url()));
  await context.route("**/*", (route) =>
    route.request().url().startsWith(origin) ? route.continue() : route.abort(),
  );
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  page.on("console", (msg) => {
    if (msg.type() === "error") console.error(msg.text());
  });
  page.on("pageerror", (e) => {
    console.error("PAGEERROR", e.message);
    errors.push(e.message);
  });
  await page.goto(origin);
  if (view === "list") {
    await page.getByRole("heading", { name: "Library", exact: true }).waitFor();
    await page.locator("#list-panel").scrollIntoViewIfNeeded();
  } else
    await page
      .getByRole("heading", { name: "Chat with your memories", exact: true })
      .waitFor();
  return { page, context, requests };
}
async function save(
  source,
  kind = "memories",
  id = "",
  version = "",
  key = crypto.randomUUID(),
) {
  const response = await fetch(url + "/api/records", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source, kind, id, version, key }),
  });
  const result = await response.json();
  assert.equal(response.status, 200, JSON.stringify(result));
  return result;
}
const source = (title, body = "A remembered afternoon.", extra = "") =>
  `---\nuser: ari\ntitle: ${title}\ncustom:\n  nested: [3, false, yes]\n${extra}---\n${body}`;
async function waitText(page, text) {
  await page
    .locator(".messages:visible")
    .getByText(text, { exact: true })
    .waitFor();
}

async function editRecord(page, title) {
  if (await page.locator('dialog[x-ref="editorDialog"][open]').count())
    await page
      .getByRole("button", { name: "Close record", exact: true })
      .click();
  await page
    .locator('dialog[x-ref="editorDialog"]')
    .waitFor({ state: "hidden" });
  await page
    .getByRole("button", { name: "Edit " + title, exact: true })
    .click();
  await page.getByRole("dialog").waitFor();
}

// Locate a rendered sphere in a screenshot, rather than exposing renderer internals.
async function graphPoint(page, color) {
  const canvas = page.locator(".graph-canvas canvas");
  const bounds = await canvas.boundingBox();
  const legend = await page.locator(".graph-legend").boundingBox();
  const png = (await canvas.screenshot()).toString("base64");
  return page.evaluate(
    async ({ png, color, bottom }) => {
      const kind = {
        green: "entities",
        amber: "memories",
        white: "topics",
        red: "stories",
      }[color];
      const target = getComputedStyle(
        document.querySelector(`[data-kind="${kind}"]`),
      )
        .backgroundColor.match(/\d+/g)
        .slice(0, 3)
        .map(Number);
      const image = new Image();
      image.src = "data:image/png;base64," + png;
      await image.decode();
      const canvas = document.createElement("canvas");
      canvas.width = image.width;
      canvas.height = image.height;
      const context = canvas.getContext("2d");
      context.drawImage(image, 0, 0);
      const pixels = context.getImageData(0, 0, image.width, image.height).data;
      let sumX = 0,
        sumY = 0,
        count = 0;
      // A wrapped legend overlays the canvas; its color keys are not nodes.
      for (let y = 15; y < image.height * bottom; y++) {
        for (let x = 15; x < image.width - 15; x++) {
          const i = (y * image.width + x) * 4;
          const [r, g, b] = pixels.slice(i, i + 3);
          if (
            Math.max(
              Math.abs(r - target[0]),
              Math.abs(g - target[1]),
              Math.abs(b - target[2]),
            ) < 24
          ) {
            sumX += x;
            sumY += y;
            count++;
          }
        }
      }
      return count ? { x: sumX / count, y: sumY / count } : null;
    },
    {
      png,
      color,
      bottom:
        Math.max(0, Math.min(bounds.height - 15, legend.y - bounds.y - 8)) /
        bounds.height,
    },
  );
}

test("serves offline bundled editors; creates and visually edits a real memory", async () => {
  const { page, context, requests } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("The blue notebook");
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page.locator(".ProseMirror").fill("A quiet afternoon with Alex.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const files = readdirSync(join(root, "store", "memories")).filter((n) =>
    n.startsWith("m_"),
  );
  assert.equal(files.length, 1);
  assert.match(
    readFileSync(
      join(root, "store", "memories", files[0], "record.md"),
      "utf8",
    ),
    /quiet afternoon/,
  );
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await page.locator(".cm-content").waitFor();
  assert.ok((await page.locator(".cm-line").count()) > 3);
  assert.match(await page.locator(".cm-content").innerText(), /created_at:/);
  await page.getByRole("tab", { name: "Visual", exact: true }).click();
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /quiet afternoon/,
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/desktop.png",
    fullPage: true,
  });
  assert.ok(requests.every((r) => r.startsWith(url)));
  await context.close();
  assert.deepEqual(errors, []);
});

test("all record types, full source, nested metadata and automatic clocks survive the UI", async () => {
  for (const kind of ["memories", "entities", "topics", "stories"]) {
    const record = await save(
      source("Source " + kind, "## A heading\n\nOriginal prose."),
      kind,
    );
    const created = record.draft.fields.find(
      (f) => f.key === "created_at",
    ).value;
    const { page, context } = await pageFor();
    await editRecord(page, "Source " + kind);
    await page.getByRole("tab", { name: "Source", exact: true }).click();
    const content = page.locator(".cm-content");
    await content.waitFor();
    const replacement = record.source
      .replace("Original prose.", "Changed prose.")
      .replace("[3, false, yes]", "[8, false, yes]");
    await content.fill(replacement);
    await page.getByRole("tab", { name: "Visual", exact: true }).click();
    assert.match(
      await page.locator(".ProseMirror").innerText(),
      /Changed prose/,
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await waitText(page, "Saved");
    const doc = await (await fetch(url + "/api/records/" + record.id)).json();
    assert.match(doc.source, /Changed prose/);
    assert.equal(
      doc.draft.fields.find((f) => f.key === "created_at").value,
      created,
    );
    assert.match(doc.source, /custom:/);
    await page.getByRole("button", { name: "Metadata", exact: true }).click();
    assert.equal(
      await page
        .getByRole("textbox", { name: "Metadata created_at", exact: true })
        .isEditable(),
      false,
    );
    await context.close();
  }
  assert.deepEqual(errors, []);
});

test("connections and deletion blockers are navigable; removing the link permits permanent deletion", async () => {
  const entity = await save(source("Alex"), "entities");
  const memory = await save(source("Meeting Alex"));
  const { page, context } = await pageFor();
  await editRecord(page, "Meeting Alex");
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page.getByLabel("Find a record", { exact: true }).fill("Alex");
  await page
    .locator(".target-results button")
    .filter({ hasText: entity.id })
    .click();
  await page
    .getByLabel("Relationship", { exact: true })
    .selectOption("related_to");
  await page
    .getByRole("button", { name: "Add connection", exact: true })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page
    .locator(".edge button")
    .filter({ hasText: "Alex" })
    .first()
    .click();
  await page.getByRole("heading", { name: "Alex", exact: true }).waitFor();
  await page
    .getByRole("button", { name: "Delete permanently", exact: true })
    .click();
  await page.locator(".blockers").waitFor();
  assert.match(await page.locator(".blockers").innerText(), /Meeting Alex/);
  await page.locator(".blockers button").click();
  await page
    .getByRole("heading", { name: "Meeting Alex", exact: true })
    .waitFor();
  if (!(await page.getByLabel("Find a record", { exact: true }).isVisible()))
    await page
      .getByRole("button", { name: "Connections", exact: true })
      .click();
  await page
    .getByRole("button", {
      name: "Remove connection " + entity.id,
      exact: true,
    })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await editRecord(page, "Alex");
  await page.getByRole("heading", { name: "Alex", exact: true }).waitFor();
  await page
    .getByRole("button", { name: "Delete permanently", exact: true })
    .click();
  await waitText(page, "Deleted permanently");
  assert.equal((await fetch(url + "/api/records/" + entity.id)).status, 404);
  await context.close();
  assert.deepEqual(errors, []);
});

test("failed acknowledgement keeps the draft and retries the original durable write once", async () => {
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Lost reply");
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page.locator(".ProseMirror").fill("A precious thought.");
  let first = true;
  await page.route("**/api/records", async (route) => {
    if (route.request().method() === "POST" && first) {
      first = false;
      await route.fetch();
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /precious thought/,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const records = await (
    await fetch(url + "/api/records?q=Lost%20reply")
  ).json();
  assert.equal(records.records.length, 1);
  await context.close();
  assert.deepEqual(errors, []);
});

test("phone layout, metadata editing, references and unsaved navigation retain the draft", async () => {
  const one = await save(
    source("Phone record", "An ordinary day.\n\n<div>Preserved raw HTML</div>"),
  );
  const two = await save(source("Another phone record"));
  const { page, context } = await pageFor({ width: 390, height: 844 });
  await editRecord(page, "Phone record");
  await page.locator(".ProseMirror p").first().click();
  await page.keyboard.press("End");
  await page.keyboard.type(" A new sentence.");
  await editRecord(page, "Another phone record");
  await editRecord(page, "Phone record");
  await page
    .getByRole("heading", { name: "Phone record", exact: true })
    .waitFor();
  assert.match(await page.locator(".ProseMirror").innerText(), /new sentence/);
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  assert.match(
    await page.locator(".cm-content").innerText(),
    /<div>Preserved raw HTML<\/div>/,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page.screenshot({
    path: "/tmp/songya-ui-review/phone.png",
    fullPage: true,
  });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("creates all four types through the visual form and exposes automatic source fields read-only", async () => {
  const { page, context } = await pageFor();
  for (const kind of ["memories", "entities", "topics", "stories"]) {
    if (await page.locator('dialog[x-ref="editorDialog"][open]').count())
      await page
        .getByRole("button", { name: "Close record", exact: true })
        .click();
    await page
      .getByRole("button", { name: "New record", exact: true })
      .first()
      .click();
    await page.getByLabel("Record type", { exact: true }).selectOption(kind);
    await page
      .getByRole("textbox", { name: "Metadata title", exact: true })
      .fill("Created " + kind);
    if (kind === "memories" || kind === "stories")
      await page
        .getByRole("textbox", { name: "Metadata user", exact: true })
        .fill("ari");
    await page.locator(".ProseMirror").fill("Created visually.");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await waitText(page, "Saved");
    await page
      .getByRole("heading", { name: "Created " + kind, exact: true })
      .waitFor();
  }
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const content = page.locator(".cm-content");
  await content.waitFor();
  const before = await content.innerText();
  await content.fill(
    before.replace(/created_at: [^\n]+/, "created_at: 1900-01-01"),
  );
  assert.equal(await content.innerText(), before);
  await context.close();
  assert.deepEqual(errors, []);
});

test("reports concurrent edits without overwriting and permits an explicit reload", async () => {
  const record = await save(source("Concurrent record"));
  const { page, context } = await pageFor();
  await editRecord(page, "Concurrent record");
  await page
    .getByRole("heading", { name: "Concurrent record", exact: true })
    .waitFor();
  await page.locator(".ProseMirror").fill("My unsaved draft.");
  await save(
    record.source.replace("A remembered afternoon.", "An external change."),
    "memories",
    record.id,
    record.version,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /My unsaved draft/,
  );
  const disk = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.match(disk.source, /external change/);
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Load saved version", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "Concurrent record", exact: true })
    .waitFor();
  await page.waitForFunction(() =>
    document
      .querySelector(".ProseMirror")
      ?.textContent.includes("external change"),
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("source and visual metadata share one draft, including new nested metadata", async () => {
  const record = await save(source("Shared draft"));
  const { page, context } = await pageFor();
  await editRecord(page, "Shared draft");
  await page
    .getByRole("heading", { name: "Shared draft", exact: true })
    .waitFor();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const code = page.getByRole("textbox", {
    name: "Markdown source",
    exact: true,
  });
  await code.waitFor();
  await code.fill(
    record.source.replace("A remembered afternoon.", "Changed in source."),
  );
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Shared edited draft");
  await page.getByRole("button", { name: "Add metadata", exact: true }).click();
  await page
    .getByLabel("Metadata name", { exact: true })
    .last()
    .fill("new_nested");
  await page
    .getByRole("combobox", { name: "Value type new_nested", exact: true })
    .selectOption("yaml");
  await page
    .locator('textarea[aria-label="Metadata new_nested"]')
    .fill("items: [3, true]\nlocation: {name: Garden}");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const saved = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.match(saved.source, /Changed in source/);
  assert.match(saved.source, /Shared edited draft/);
  assert.match(saved.source, /new_nested:/);
  assert.match(saved.source, /Garden/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("a malformed draft is retained while healthy records stay editable", async () => {
  const bad = await save(source("Draft with a mistake"));
  const healthy = await save(source("Healthy neighbour"));
  const { page, context } = await pageFor();
  await editRecord(page, "Draft with a mistake");
  await page
    .getByRole("heading", { name: "Draft with a mistake", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  await page
    .locator('textarea[aria-label="Metadata custom"]')
    .fill("broken: [unfinished");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await editRecord(page, "Healthy neighbour");
  await page
    .getByRole("heading", { name: "Healthy neighbour", exact: true })
    .waitFor();
  await page.locator(".ProseMirror").fill("Still editable.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await editRecord(page, "Draft with a mistake");
  await page
    .getByRole("heading", { name: "Draft with a mistake", exact: true })
    .waitFor();
  if (
    !(await page.locator('textarea[aria-label="Metadata custom"]').isVisible())
  )
    await page.getByRole("button", { name: "Metadata", exact: true }).click();
  assert.equal(
    await page.locator('textarea[aria-label="Metadata custom"]').inputValue(),
    "broken: [unfinished",
  );
  await page
    .locator('textarea[aria-label="Metadata custom"]')
    .fill("fixed: [3, false]");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  assert.match(
    (await (await fetch(url + "/api/records/" + bad.id)).json()).source,
    /fixed:/,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("quoted automatic timestamps are protected and malformed records leave healthy reads usable", async (t) => {
  const damagedPath = join(
    root,
    "store",
    "memories",
    "m_000000000098/record.md",
  );
  t.after(() => rmSync(dirname(damagedPath), { force: true, recursive: true }));
  const id = "m_000000000099";
  writeFileSync(
    join(root, "store", "memories", id + "/record.md"),
    '---\r\nuser: ari\r\ntitle: Quoted clocks\r\n"created_at": 2024-01-01T00:00:00Z\r\n"updated_at": 2024-01-01T00:00:00Z\r\n---\r\nA record.',
  );
  writeFileSync(damagedPath, "---\ntitle: [broken\n---\nRetained prose.");
  const { page, context } = await pageFor();
  await editRecord(page, "Quoted clocks");
  await page
    .getByRole("heading", { name: "Quoted clocks", exact: true })
    .waitFor();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const editor = page.getByRole("textbox", {
    name: "Markdown source",
    exact: true,
  });
  await editor.waitFor();
  const original = await editor.innerText();
  await editor.fill(
    original.replace("2024-01-01T00:00:00Z", "1900-01-01T00:00:00Z"),
  );
  assert.equal(await editor.innerText(), original);
  await editor.fill(original.replace("A record.", "A healthy edit."));
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  assert.match(
    (await (await fetch(url + "/api/records/" + id)).json()).source,
    /healthy edit/,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("owner and type filters preserve shared identities without adding access restrictions", async () => {
  await save(source("Ownership Bob"), "memories").then(async (record) => {
    await save(
      record.source.replace("user: ari", "user: bob"),
      "memories",
      record.id,
      record.version,
    );
  });
  await save(source("Ownership Ari"), "memories");
  await save(source("Ownership shared"), "entities");
  const { page, context } = await pageFor();
  await page.locator(".library-filters > summary").click();
  await page
    .getByRole("searchbox", { name: "Search records", exact: true })
    .fill("Ownership");
  await page.getByLabel("Owner", { exact: true }).selectOption("bob");
  await page.waitForFunction(
    () => document.querySelectorAll(".record").length === 2,
  );
  const rows = await page.locator(".record").allTextContents();
  assert.ok(rows.some((r) => r.includes("Ownership Bob")));
  assert.ok(rows.some((r) => r.includes("Ownership shared")));
  assert.ok(!rows.some((r) => r.includes("Ownership Ari")));
  await page
    .getByLabel("Record type filter", { exact: true })
    .selectOption("memories");
  await page.waitForFunction(
    () => document.querySelectorAll(".record").length === 1,
  );
  assert.match(await page.locator(".record").innerText(), /Ownership Bob/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("edits after a lost reply resolve the original write before saving the new draft", async () => {
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("A changing draft");
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page
    .getByRole("textbox", { name: "Record text", exact: true })
    .fill("Original thought.");
  let first = true;
  await page.route("**/api/records", async (route) => {
    if (route.request().method() === "POST" && first) {
      first = false;
      await route.fetch();
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await page
    .getByRole("textbox", { name: "Record text", exact: true })
    .fill("A newer thought.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(
    page,
    "Previous save confirmed. Your newer changes are still unsaved.",
  );
  assert.match(
    await page
      .getByRole("textbox", { name: "Record text", exact: true })
      .innerText(),
    /newer thought/,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const result = await (
    await fetch(url + "/api/records?q=A%20changing%20draft")
  ).json();
  const matching = result.records.filter((r) => r.title === "A changing draft");
  assert.equal(matching.length, 1);
  const saved = await (
    await fetch(url + "/api/records/" + matching[0].id)
  ).json();
  assert.match(saved.source, /newer thought/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("discarding a new draft creates no record", async () => {
  const before = await (await fetch(url + "/api/records")).json();
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Never saved");
  page.once("dialog", (d) => d.accept());
  await page
    .getByRole("button", { name: "Discard draft", exact: true })
    .click();
  await waitText(page, "Draft discarded");
  const after = await (await fetch(url + "/api/records")).json();
  assert.equal(after.records.length, before.records.length);
  await context.close();
  assert.deepEqual(errors, []);
});

test("a rejected save can be corrected and saved without retaining the invalid request", async () => {
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Corrected owner");
  await page.locator(".ProseMirror").fill("A draft without its owner yet.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const listed = await (
    await fetch(url + "/api/records?q=Corrected%20owner")
  ).json();
  assert.equal(
    listed.records.filter((r) => r.title === "Corrected owner").length,
    1,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("the front page is a sortable metadata table with independent column filters and read-only inspection", async () => {
  const policy = await (await fetch(url + "/api/types/ari")).json();
  policy.kinds.memories.push({
    id: "experience",
    name: "Experience",
    description: "An experience account.",
  });
  const configured = await fetch(url + "/api/types/ari", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      key: crypto.randomUUID(),
      version: policy.version,
      kinds: policy.kinds,
    }),
  });
  assert.equal(configured.status, 200);
  for (const [title, description, type, alias, kind] of [
    ["Table 2", "Cedar tree", "plant", "Atlas", "entities"],
    ["Table 10", "Botanist", "person", "Zoe", "entities"],
    ["Table 1", "A walk outdoors", "experience", "Cedar walk", "memories"],
  ])
    await save(
      source(
        title,
        kind === "memories" ? "# Inspectable prose" : description,
        `${kind === "memories" ? `description: ${description}\n` : ""}types: {ari: ${type}}\naliases: [${alias}]\n`,
      ),
      kind,
    );
  const { page, context } = await pageFor();
  await page.locator(".library-filters > summary").click();
  await page.getByLabel("Filter title", { exact: true }).fill("Table ");
  assert.equal(await page.locator(".record").count(), 3);
  assert.equal(await page.locator("aside").count(), 0);
  assert.equal(await page.locator("dialog:modal").count(), 0);
  const titles = () =>
    page.locator(".title-cell .text-button").allTextContents();
  assert.deepEqual(await titles(), ["Table 1", "Table 10", "Table 2"]);
  await page
    .getByRole("button", { name: "Sort by Title", exact: true })
    .click();
  assert.deepEqual(await titles(), ["Table 1", "Table 2", "Table 10"]);
  await page
    .getByRole("button", { name: "Sort by Title", exact: true })
    .click();
  assert.deepEqual(await titles(), ["Table 10", "Table 2", "Table 1"]);
  assert.equal(
    await page.getByRole("columnheader").first().getAttribute("aria-sort"),
    "descending",
  );
  for (const column of [
    "Description",
    "Record type",
    "Type",
    "Aliases",
    "Owner",
  ]) {
    await page
      .getByRole("button", { name: "Sort by " + column, exact: true })
      .click();
    const header = page.getByRole("columnheader").filter({
      has: page.getByRole("button", {
        name: "Sort by " + column,
        exact: true,
      }),
    });
    assert.equal(await header.getAttribute("aria-sort"), "ascending");
    await page
      .getByRole("button", { name: "Sort by " + column, exact: true })
      .click();
    assert.equal(await header.getAttribute("aria-sort"), "descending");
  }
  await page.getByLabel("Filter description", { exact: true }).fill("cedar");
  assert.deepEqual(await titles(), ["Table 2"]);
  await page.getByLabel("Filter description", { exact: true }).fill("");
  await page.getByLabel("Filter aliases", { exact: true }).fill("zoe");
  assert.deepEqual(await titles(), ["Table 10"]);
  await page.getByLabel("Filter aliases", { exact: true }).fill("");
  await page
    .getByLabel("Metadata type filter", { exact: true })
    .selectOption("plant");
  assert.deepEqual(await titles(), ["Table 2"]);
  await page.getByRole("button", { name: "Table 2", exact: true }).click();
  const modal = page.getByRole("dialog");
  await modal.waitFor();
  assert.equal(
    await modal.locator(".ProseMirror").getAttribute("contenteditable"),
    "false",
  );
  assert.match(
    await modal.locator(".inspection-metadata").innerText(),
    /plant/,
  );
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  assert.equal(
    await modal.locator(".cm-content").getAttribute("contenteditable"),
    "false",
  );
  await modal.getByRole("button", { name: "Edit", exact: true }).click();
  assert.equal(
    await modal.locator(".cm-content").getAttribute("contenteditable"),
    "true",
  );
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  assert.deepEqual(await titles(), ["Table 2"]);
  await page.getByRole("button", { name: "Edit Table 2", exact: true }).click();
  await modal.waitFor();
  assert.equal(
    await modal.locator(".ProseMirror").getAttribute("contenteditable"),
    "true",
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/table-modal.png",
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await page
    .getByLabel("Metadata type filter", { exact: true })
    .selectOption("");
  await page.screenshot({
    path: "/tmp/songya-ui-review/table.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  assert.ok(
    await page
      .locator(".table-scroll")
      .evaluate((el) => el.scrollWidth > el.clientWidth),
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/table-phone.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Edit Table 2", exact: true }).click();
  await modal.waitFor();
  const bounds = await modal.boundingBox();
  assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= 390);
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await modal.waitFor({ state: "hidden" });
  await context.close();
  assert.deepEqual(errors, []);
});

test("table deletion shows navigable blockers and removes only an unreferenced record", async () => {
  const target = await save(source("Table deletion target"), "entities");
  const incoming = await save(
    source(
      "Table deletion source",
      "A linked event.",
      `relations: [{type: related_to, target: ${target.id}}]\n`,
    ),
  );
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "Delete Table deletion target", exact: true })
    .click();
  await page.getByRole("dialog").waitFor();
  await page
    .locator(".blockers button")
    .filter({ hasText: "Table deletion source" })
    .click();
  await page
    .getByRole("heading", { name: "Table deletion source", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page
    .getByRole("button", {
      name: "Remove connection " + target.id,
      exact: true,
    })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("button", { name: "Delete Table deletion target", exact: true })
    .click();
  await waitText(page, "Deleted permanently");
  assert.equal((await fetch(url + "/api/records/" + target.id)).status, 404);
  assert.equal((await fetch(url + "/api/records/" + incoming.id)).status, 200);
  await context.close();
  assert.deepEqual(errors, []);
});

test("backdrop and Escape close the modal while keeping drafts locally", async () => {
  const record = await save(source("Modal dismissal"));
  const { page, context } = await pageFor();
  await editRecord(page, "Modal dismissal");
  const modal = page.getByRole("dialog");
  await page.locator(".ProseMirror").fill("An unsaved local draft.");
  // Closing must not depend on composing the draft on the server.
  await page.route("**/api/draft", (route) => route.abort());
  await modal
    .getByRole("heading", { name: "Modal dismissal", exact: true })
    .click();
  assert.equal(await modal.isVisible(), true);
  await page.mouse.click(4, 4);
  await modal.waitFor({ state: "hidden" });
  await editRecord(page, "Modal dismissal");
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /unsaved local draft/,
  );
  await page.unroute("**/api/draft");
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await page.locator(".cm-content").focus();
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await editRecord(page, "Modal dismissal");
  assert.match(
    await page.locator(".cm-content").innerText(),
    /unsaved local draft/,
  );
  await page.getByRole("tab", { name: "Visual", exact: true }).click();
  await page.locator(".ProseMirror").focus();
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await page
    .getByRole("button", { name: "Modal dismissal", exact: true })
    .click();
  await modal.waitFor();
  await page.mouse.click(4, 4);
  await modal.waitFor({ state: "hidden" });
  const stored = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.equal(stored.source, record.source);
  await context.close();
  assert.deepEqual(errors, []);
});

test("graph views render, preserve search when switching, and open safe hover labels in the inspector", async () => {
  const title = "Graph <b>Alex</b>";
  const entity = await save(
    source(title, "", "aliases: [GraphAlias]\n"),
    "entities",
  );
  const topic = await save(source("Graph topic"), "topics");
  const memory = await save(
    source(
      "Graph memory",
      "A connected afternoon.",
      `relations: [{type: related_to, target: ${entity.id}}, {type: related_to, target: ${topic.id}}]\n`,
    ),
  );
  const story = await save(
    source(
      "Graph story",
      "",
      `relations: [{type: related_to, target: ${memory.id}}]\n`,
    ),
    "stories",
  );
  const ids = new Set([entity.id, topic.id, memory.id, story.id]);
  const data = await (await fetch(url + "/api/graph")).json();
  data.nodes = data.nodes.filter((node) => ids.has(node.id));
  data.links = data.links.filter((link) => ids.has(link.source));
  data.links.push({ source: memory.id, target: "m_missing", type: "about" });
  const { page, context } = await pageFor();
  await page.route("**/api/graph**", (route) => route.fulfill({ json: data }));
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  await page.waitForTimeout(3000); // Let the force layout settle and the camera fit.
  const graph = page.getByRole("region", { name: "Memory graph", exact: true });
  const bounds = await graph.boundingBox();
  assert.ok(bounds.height > 0 && bounds.height < 450);
  const views = page.getByRole("group", { name: "Graph view", exact: true });
  assert.equal(
    await views
      .getByRole("button", { name: "2D", exact: true })
      .getAttribute("aria-pressed"),
    "true",
  );
  const expand = page.getByRole("button", {
    name: "Expand graph to full screen",
    exact: true,
  });
  await expand.click();
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .height === innerHeight,
  );
  assert.deepEqual(await graph.boundingBox(), {
    x: 0,
    y: 0,
    width: 1320,
    height: 900,
  });
  assert.equal(await page.evaluate(() => document.fullscreenElement), null);
  assert.equal(
    await page.locator("#chat-panel").evaluate((el) => el.inert),
    true,
  );
  await page.keyboard.press("Escape");
  await page.waitForFunction(
    (height) =>
      Math.abs(
        document.querySelector(".graph-canvas canvas").getBoundingClientRect()
          .height - height,
      ) < 1,
    bounds.height,
  );
  assert.equal(
    await page.locator("#chat-panel").evaluate((el) => el.inert),
    false,
  );
  await expand.click();
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .height === innerHeight,
  );
  await page.waitForTimeout(500); // Let resizing update both drawing and hit testing.
  const expandedPoint = await graphPoint(page, "green");
  await page.mouse.move(expandedPoint.x, expandedPoint.y);
  await page
    .locator(".float-tooltip-kap")
    .getByText(title, { exact: true })
    .waitFor();
  await page.mouse.click(expandedPoint.x, expandedPoint.y);
  await page
    .getByRole("dialog")
    .getByRole("heading", { name: title, exact: true })
    .waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    await page
      .getByRole("button", { name: "Exit full-screen graph", exact: true })
      .isVisible(),
    true,
  );
  await page
    .getByRole("button", { name: "Exit full-screen graph", exact: true })
    .click();
  await page.waitForTimeout(500);
  assert.equal(
    await graph.evaluate((el) => getComputedStyle(el).backgroundColor),
    "rgb(220, 224, 232)",
  );
  for (const color of ["green", "amber", "white", "red"]) {
    assert.ok(
      await graphPoint(page, color),
      "Missing rendered " + color + " sphere",
    );
  }
  const point = await graphPoint(page, "green");
  const canvas = await page.locator(".graph-canvas canvas").boundingBox();
  await page.mouse.move(canvas.x + point.x, canvas.y + point.y);
  const tooltip = page.locator(".float-tooltip-kap");
  await tooltip.getByText(title, { exact: true }).waitFor();
  assert.equal(await tooltip.locator("b").count(), 0);
  await page.mouse.click(canvas.x + point.x, canvas.y + point.y);
  const modal = page.getByRole("dialog");
  await modal.getByRole("heading", { name: title, exact: true }).waitFor();
  assert.equal(
    await modal.locator(".ProseMirror").getAttribute("contenteditable"),
    "false",
  );
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  const search = page.getByRole("searchbox", {
    name: "Search graph",
    exact: true,
  });
  const results = page.locator("#graph-matches");
  const rowCount = await page.locator("tbody tr.record").count();
  await search.fill("gRaPhAlIaS");
  await results
    .getByRole("button", { name: title + " entities", exact: true })
    .waitFor();
  assert.equal(await results.locator("button").count(), 1);
  assert.equal(await page.locator("tbody tr.record").count(), rowCount);
  await search.press("Enter");
  await results.waitFor({ state: "hidden" });
  await page.waitForTimeout(750); // Camera transition finishes at 650ms.
  const focused = await graphPoint(page, "green");
  assert.ok(Math.abs(focused.x - canvas.width / 2) < 15);
  assert.ok(Math.abs(focused.y - canvas.height / 2) < 15);
  assert.equal(await graphPoint(page, "red"), null);
  await page.mouse.move(canvas.x + focused.x, canvas.y + focused.y);
  await tooltip.getByText(title, { exact: true }).waitFor();
  await page.mouse.click(canvas.x + focused.x, canvas.y + focused.y);
  await modal.getByRole("heading", { name: title, exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await search.fill("no-such-record");
  await results.getByText("No matches", { exact: true }).waitFor();
  await search.fill(entity.id);
  await results
    .getByRole("button", { name: title + " entities", exact: true })
    .click();
  await results.waitFor({ state: "hidden" });
  await search.focus();
  await search.press("Escape");
  assert.equal(await search.inputValue(), "");
  await page.waitForTimeout(750); // The overview finishes after the earlier focus.
  for (const color of ["green", "amber", "white", "red"]) {
    assert.ok(await graphPoint(page, color), "Clear did not restore " + color);
  }
  await search.fill("GraphAlias");
  for (const mode of ["2D", "3D"]) {
    await views.getByRole("button", { name: mode, exact: true }).click();
    assert.equal(
      await views
        .getByRole("button", { name: mode, exact: true })
        .getAttribute("aria-pressed"),
      "true",
    );
    assert.equal(await search.inputValue(), "GraphAlias");
    assert.equal(await page.locator(".graph-canvas canvas").count(), 1);
    assert.equal(
      await page
        .locator(".graph-canvas canvas")
        .evaluate(
          (canvas, mode) =>
            !!canvas.getContext(mode === "2D" ? "2d" : "webgl2"),
          mode,
        ),
      true,
    );
    await page.waitForTimeout(3000);
    await search.focus();
    await search.press("Enter");
    await page.waitForTimeout(750);
    const point = await graphPoint(page, "green");
    assert.ok(
      Math.abs(point.x - canvas.width / 2) < 15,
      mode + " search did not center x",
    );
    assert.ok(
      Math.abs(point.y - canvas.height / 2) < 15,
      mode + " search did not center y",
    );
    assert.equal(await graphPoint(page, "red"), null);
    await page.mouse.move(canvas.x + point.x, canvas.y + point.y);
    await tooltip.getByText(title, { exact: true }).waitFor();
    assert.equal(await tooltip.locator("b").count(), 0);
    await page.mouse.click(canvas.x + point.x, canvas.y + point.y);
    await modal.getByRole("heading", { name: title, exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await modal.waitFor({ state: "hidden" });
    if (mode === "2D") {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.waitForFunction(
        () =>
          document.querySelector(".graph-canvas canvas").getBoundingClientRect()
            .width === document.querySelector(".graph-canvas").clientWidth,
      );
      const input = await search.boundingBox();
      const toggle = await views.boundingBox();
      assert.ok(
        input.y >= toggle.y + toggle.height ||
          input.x + input.width <= toggle.x,
      );
      await expand.click();
      await page.waitForFunction(
        () =>
          document.querySelector(".graph-canvas canvas").getBoundingClientRect()
            .height === innerHeight,
      );
      assert.deepEqual(await graph.boundingBox(), {
        x: 0,
        y: 0,
        width: 390,
        height: 844,
      });
      assert.equal(await search.inputValue(), "GraphAlias");
      const controls = await page.locator(".graph-tools").boundingBox();
      assert.ok(controls.x >= 0 && controls.x + controls.width <= 390);
      await page.screenshot({
        path: "/tmp/songya-ui-review/graph-expanded-phone.png",
        fullPage: true,
      });
      await page.keyboard.press("Escape");
      await page.waitForFunction(
        () =>
          document.querySelector(".graph-canvas canvas").getBoundingClientRect()
            .height === document.querySelector(".graph-canvas").clientHeight,
      );
      await page.screenshot({
        path: "/tmp/songya-ui-review/graph-2d-phone.png",
        fullPage: true,
      });
      await page.setViewportSize({ width: 1320, height: 900 });
    }
  }
  await search.focus();
  await search.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .width === document.querySelector(".graph-canvas").clientWidth,
  );
  assert.ok(
    (await graph.boundingBox()).height > 0 &&
      (await graph.boundingBox()).height < 422,
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph-phone.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1320, height: 900 });
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph.png",
    fullPage: true,
  });
  await context.close();
  assert.deepEqual(errors, []);
});

test("graph owner selection keeps only that owner's accounts and connected shared identities", async () => {
  const shared = await save(source("Owner graph shared"), "entities");
  const topic = await save(source("Owner graph topic"), "topics");
  const unrelated = await save(source("Owner graph unrelated"), "entities");
  const ari = await save(
    source(
      "Owner graph Ari",
      "Ari's account.",
      `relations: [{type: related_to, target: ${shared.id}}, {type: related_to, target: ${topic.id}}]\n`,
    ),
  );
  const clara = await save(
    source(
      "Owner graph Clara",
      "Clara's account.",
      `relations: [{type: related_to, target: ${shared.id}}]\n`,
    ).replace("user: ari", "user: clara"),
  );
  const ariStory = await save(
    source(
      "Owner graph Ari story",
      "",
      `relations: [{type: related_to, target: ${ari.id}}]\n`,
    ),
    "stories",
  );
  const claraStory = await save(
    source(
      "Owner graph Clara story",
      "",
      `relations: [{type: related_to, target: ${clara.id}}]\n`,
    ).replace("user: ari", "user: clara"),
    "stories",
  );
  const incoming = await save(
    source(
      "Owner graph incoming",
      "",
      `relations: [{type: related_to, target: ${ari.id}}]\n`,
    ),
    "entities",
  );
  const ids = new Set([
    shared.id,
    topic.id,
    unrelated.id,
    ari.id,
    clara.id,
    ariStory.id,
    claraStory.id,
    incoming.id,
  ]);
  const data = await (await fetch(url + "/api/graph")).json();
  data.nodes = data.nodes.filter((node) => ids.has(node.id));
  data.links = data.links.filter(
    (link) => ids.has(link.source) && ids.has(link.target),
  );
  const { page, context } = await pageFor();
  await page.route("**/api/graph**", (route) => route.fulfill({ json: data }));
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  const owner = page.getByRole("combobox", {
    name: "Graph memory owner",
    exact: true,
  });
  assert.equal(await owner.inputValue(), "");
  assert.deepEqual(await owner.locator("option").allTextContents(), [
    "Any",
    "ari",
    "clara",
  ]);
  const search = page.getByRole("searchbox", {
    name: "Search graph",
    exact: true,
  });
  await search.fill("Owner graph");
  const matches = () =>
    page.locator("#graph-matches button span").allTextContents();
  assert.equal((await matches()).length, 8);
  const rows = await page.locator("tbody tr.record").count();
  await owner.selectOption("ari");
  await search.focus();
  assert.deepEqual(await matches(), [
    "Owner graph Ari",
    "Owner graph Ari story",
    "Owner graph incoming",
    "Owner graph shared",
    "Owner graph topic",
  ]);
  assert.equal(
    await page.getByLabel("Owner", { exact: true }).inputValue(),
    "",
  );
  assert.equal(await page.locator("tbody tr.record").count(), rows);
  await owner.selectOption("clara");
  await search.focus();
  assert.deepEqual(await matches(), [
    "Owner graph Clara",
    "Owner graph Clara story",
    "Owner graph shared",
  ]);
  await page
    .getByRole("group", { name: "Graph view", exact: true })
    .getByRole("button", { name: "3D", exact: true })
    .click();
  assert.equal(await owner.inputValue(), "clara");
  await search.focus();
  assert.equal((await matches()).length, 3);
  await page
    .getByRole("group", { name: "Graph view", exact: true })
    .getByRole("button", { name: "2D", exact: true })
    .click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page
    .getByRole("button", { name: "Expand graph to full screen", exact: true })
    .click();
  await owner.selectOption("ari");
  await search.focus();
  assert.equal((await matches()).length, 5);
  const controlBounds = await page.locator(".graph-tools").boundingBox();
  const searchBounds = await search.boundingBox();
  assert.ok(
    controlBounds.x >= 0 && controlBounds.x + controlBounds.width <= 390,
  );
  assert.ok(
    searchBounds.y >= controlBounds.y + controlBounds.height ||
      searchBounds.x + searchBounds.width <= controlBounds.x,
  );
  await page.waitForTimeout(3000); // Settle the selected owner's layout before checking rendered nodes.
  await page.mouse.click(386, 422); // Dismiss the results overlay without clearing the query.
  assert.ok(
    await graphPoint(page, "amber"),
    "Selected owner's memory did not render",
  );
  assert.ok(
    await graphPoint(page, "red"),
    "Selected owner's story did not render",
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph-owner-phone.png",
  });
  await owner.selectOption("");
  await search.focus();
  assert.equal((await matches()).length, 8);
  await context.close();
  assert.deepEqual(errors, []);
});

test("dense 2D graphs render separate disks for every node", async () => {
  const nodes = Array.from({ length: 90 }, (_, i) => ({
    id: "e_dense_" + i,
    title: "Dense node " + i,
    kind: "entities",
  }));
  const links = [];
  for (let i = 5; i < nodes.length; i++) {
    for (let hub = 0; hub < 5; hub++)
      links.push({
        source: nodes[i].id,
        target: nodes[hub].id,
        type: "related_to",
      });
  }
  const { page, context } = await pageFor();
  await page.route("**/api/graph**", (route) =>
    route.fulfill({ json: { nodes, links } }),
  );
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  await page.waitForTimeout(3500);
  const png = (
    await page.locator(".graph-canvas canvas").screenshot()
  ).toString("base64");
  const canvasBounds = await page.locator(".graph-canvas canvas").boundingBox();
  const legendBounds = await page.locator(".graph-legend").boundingBox();
  const disks = await page.evaluate(
    async ({ png, bottom }) => {
      const image = new Image();
      image.src = "data:image/png;base64," + png;
      await image.decode();
      const canvas = document.createElement("canvas");
      canvas.width = image.width;
      canvas.height = image.height;
      const ctx = canvas.getContext("2d");
      ctx.drawImage(image, 0, 0);
      const { data } = ctx.getImageData(0, 0, image.width, image.height);
      const green = new Uint8Array(image.width * image.height);
      // Exclude the actual legend area, including its weight-status row.
      for (let i = 0; i < image.width * Math.floor(image.height * bottom); i++)
        green[i] =
          data[i * 4 + 1] - Math.max(data[i * 4], data[i * 4 + 2]) > 50;
      let disks = 0;
      for (let start = 0; start < green.length; start++) {
        if (!green[start]) continue;
        let pixels = 0;
        const pending = [start];
        green[start] = 0;
        while (pending.length) {
          const index = pending.pop();
          pixels++;
          const x = index % image.width,
            y = Math.floor(index / image.width);
          for (let dy = -1; dy <= 1; dy++)
            for (let dx = -1; dx <= 1; dx++) {
              if (
                x + dx < 0 ||
                x + dx >= image.width ||
                y + dy < 0 ||
                y + dy >= image.height
              )
                continue;
              const next = index + dy * image.width + dx;
              if (green[next]) {
                green[next] = 0;
                pending.push(next);
              }
            }
        }
        if (pixels > 3) disks++;
      }
      return disks;
    },
    { png, bottom: (legendBounds.y - canvasBounds.y) / canvasBounds.height },
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph-dense-2d.png",
    fullPage: true,
  });
  assert.equal(
    disks,
    nodes.length,
    "Some rendered nodes overlap or are clipped",
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("WebGL failure leaves the library and inspector working", async () => {
  await save(source("No WebGL", "The library remains usable."));
  const { page, context } = await pageFor(undefined, () => {
    const original = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (type, ...args) {
      if (type.startsWith("webgl")) return null;
      return original.call(this, type, ...args);
    };
  });
  // The default view works without WebGL; only an explicit 3D choice needs it.
  await page.locator(".graph-canvas canvas").waitFor();
  assert.equal(
    await page
      .locator(".graph-canvas canvas")
      .evaluate((canvas) => !!canvas.getContext("2d")),
    true,
  );
  await page
    .getByRole("group", { name: "Graph view", exact: true })
    .getByRole("button", { name: "3D", exact: true })
    .click();
  await page
    .getByText("Graph unavailable. You can still browse the library.", {
      exact: true,
    })
    .waitFor();
  await editRecord(page, "No WebGL");
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /library remains usable/,
  );
  await page.keyboard.press("Escape");
  await page
    .getByRole("group", { name: "Graph view", exact: true })
    .getByRole("button", { name: "2D", exact: true })
    .click();
  await page.waitForFunction(
    () => !window.Alpine.$data(document.body).graphStatus,
  );
  assert.equal(
    await page
      .locator(".graph-canvas canvas")
      .evaluate((canvas) => !!canvas.getContext("2d")),
    true,
  );
  const search = page.getByRole("searchbox", {
    name: "Search graph",
    exact: true,
  });
  await search.fill("No WebGL");
  await page
    .locator("#graph-matches")
    .getByRole("button", { name: "No WebGL memories", exact: true })
    .waitFor();
  await context.close();
  assert.deepEqual(errors, []);
});

test("date controls preserve exact dates, open bounds and unknown values across editor modes", async () => {
  const record = await save(
    source(
      "Event time controls",
      "A dated event.",
      'time: {date: "2024-12-31", earliest: null, latest: null}\n',
    ),
  );
  const { page, context } = await pageFor();
  await editRecord(page, "Event time controls");
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  assert.equal(
    await page.getByLabel("Exact event date", { exact: true }).inputValue(),
    "2024-12-31",
  );
  await page
    .getByLabel("Earliest event date", { exact: true })
    .fill("2024-01-01");
  await page.getByLabel("Earliest event date", { exact: true }).blur();
  assert.equal(
    await page.getByLabel("Exact event date", { exact: true }).inputValue(),
    "",
  );
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  assert.match(await page.locator(".cm-content").innerText(), /2024-01-01/);
  await page.getByRole("tab", { name: "Visual", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  let doc = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.deepEqual(doc.draft.fields.find((f) => f.key === "time").time, {
    date: null,
    earliest: "2024-01-01",
    latest: null,
  });
  await page.getByLabel("Earliest event date", { exact: true }).fill("");
  await page.getByLabel("Earliest event date", { exact: true }).blur();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  doc = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.deepEqual(doc.draft.fields.find((f) => f.key === "time").time, {
    date: null,
    earliest: null,
    latest: null,
  });
  await context.close();
  assert.deepEqual(errors, []);
});

test("story relation controls persist reading order while preserving prose", async () => {
  const first = await save(source("Reading first"));
  const second = await save(source("Reading second"));
  const story = await save(
    source(
      "Reading order",
      "Introduction",
      `relations: [{type: related_to, target: ${first.id}}, {type: related_to, target: ${second.id}}]\n`,
    ),
    "stories",
  );
  const { page, context } = await pageFor();
  await editRecord(page, "Reading order");
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page
    .locator(".edge")
    .filter({ hasText: "Reading second" })
    .getByRole("button", { name: "Move connection up", exact: true })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const doc = await (await fetch(url + "/api/records/" + story.id)).json();
  const ordered = doc.source.match(/target: (m_[a-z0-9]{12})/g);
  assert.deepEqual(ordered, ["target: " + second.id, "target: " + first.id]);
  assert.equal(doc.draft.body, "Introduction");
  await context.close();
  assert.deepEqual(errors, []);
});

async function chatProvider(t, completion) {
  let calls = 0;
  const server = createServer(async (req, res) => {
    if (req.url === "/v1/models") {
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          object: "list",
          data: [
            {
              id: "browser-model",
              object: "model",
              owned_by: "test",
              created: 1,
            },
          ],
        }),
      );
      return;
    }
    try {
      assert.equal(req.url, "/v1/chat/completions");
      let text = "";
      for await (const chunk of req) text += chunk;
      const body = JSON.parse(text);
      assert.equal(body.stream, true);
      assert.equal(body.model, "browser-model");
      assert.ok(body.tools.length >= 20);
      await completion(res, body, ++calls);
    } catch (e) {
      errors.push(e.message);
      res.destroy();
    }
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  return {
    base: `http://127.0.0.1:${server.address().port}/v1`,
    calls: () => calls,
  };
}
function streamChunk(res, delta, reason = null) {
  if (!res.headersSent) res.setHeader("Content-Type", "text/event-stream");
  res.write(
    `data: ${JSON.stringify({ id: "browser-completion", object: "chat.completion.chunk", created: 1, model: "browser-model", choices: [{ index: 0, delta, finish_reason: reason }] })}\n\n`,
  );
}
function streamFinish(res, reason = "stop") {
  streamChunk(res, {}, reason);
  res.end("data: [DONE]\n\n");
}
function streamTool(res, name, args) {
  const text = JSON.stringify(args),
    mid = Math.floor(text.length / 2);
  streamChunk(res, {
    role: "assistant",
    tool_calls: [
      {
        index: 0,
        id: "browser_" + name,
        type: "function",
        function: { name, arguments: text.slice(0, mid) },
      },
    ],
  });
  streamChunk(res, {
    tool_calls: [{ index: 0, function: { arguments: text.slice(mid) } }],
  });
  streamFinish(res, "tool_calls");
}
async function setupBrowserChat(page, base, user) {
  await page.getByRole("button", { name: "Add user", exact: true }).click();
  await page.getByLabel("New chat user", { exact: true }).fill(user);
  await page
    .getByRole("button", { name: "Use this user", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Chat settings", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("API base URL", { exact: true }).fill(base);
  await dialog.getByLabel("API key", { exact: true }).fill("browser-test-key");
  await dialog.getByLabel("Model", { exact: true }).fill("browser-model");
  await dialog.getByLabel("Temperature", { exact: true }).fill("0.15");
  assert.equal(
    await dialog.getByLabel("Temperature", { exact: true }).inputValue(),
    "0.15",
  );
  await dialog
    .getByRole("button", {
      name: "Test connection and load models",
      exact: true,
    })
    .click();
  await dialog
    .getByText("Connected. 1 models available.", { exact: true })
    .waitFor();
  assert.equal(
    await dialog.getByLabel("API key", { exact: true }).inputValue(),
    "",
  );
  await dialog
    .getByRole("button", { name: "Save connection", exact: true })
    .click();
  await dialog.waitFor({ state: "hidden" });
}

test("chat defaults on, streams real MCP writes, follows replies and restores user history", async (t) => {
  let release, next;
  const finish = new Promise((resolve) => {
    release = resolve;
  });
  const more = new Promise((resolve) => {
    next = resolve;
  });
  t.after(() => {
    release();
    next();
  });
  const provider = await chatProvider(t, async (res, body, n) => {
    assert.equal(body.temperature, 0.15);
    assert.equal(body.parallel_tool_calls, false);
    if (n === 1)
      return streamTool(res, "prepare_memory", {
        text: "I moved to the island on 12 June 2001.",
      });
    if (n === 2)
      return streamTool(res, "save_memory", {
        title: "Move to the island on 12 June 2001",
        text: "I moved to the island on 12 June 2001.",
        time: { date: "2001-06-12" },
      });
    assert.match(body.messages.at(-1).content, /"outcome":"created"/);
    streamChunk(res, {
      role: "assistant",
      content:
        "**Saved your move.**\n\n" +
        Array.from(
          { length: 40 },
          (_, i) => `Paragraph ${i}: A remembered moment.\n\n`,
        ).join(""),
    });
    await more;
    streamChunk(res, { content: "More details arrived.\n\n" });
    await finish;
    streamChunk(res, {
      content:
        'Reply completed. <img src=x onerror="window.chatInjected=true">',
    });
    streamFinish(res);
  });
  const { page, context } = await pageFor(undefined, undefined, "chat");
  t.after(() => context.close());
  assert.equal(await page.locator("#chat-panel").isVisible(), true);
  assert.equal(await page.locator("#list-panel").isVisible(), true);
  assert.equal(
    await page.getByRole("tablist", { name: "Memory workspace" }).count(),
    0,
  );
  await setupBrowserChat(page, provider.base, "browser-chat-person");
  const observer = await pageFor(undefined, undefined, "chat");
  t.after(() => observer.context.close());
  await observer.page.locator(".graph-canvas canvas").waitFor();
  let failedGraphRefresh = false,
    failedListRefresh = false;
  await observer.page.route("**/api/graph**", (route) => {
    if (!failedGraphRefresh) {
      failedGraphRefresh = true;
      return route.abort();
    }
    return route.continue();
  });
  await observer.page.route("**/api/records?**", (route) => {
    if (!failedListRefresh) {
      failedListRefresh = true;
      return route.abort();
    }
    return route.continue();
  });
  await page
    .getByLabel("Message Songya", { exact: true })
    .fill("Remember that I moved to the island on 12 June 2001.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  const log = page.getByRole("log", { name: "Chat messages", exact: true });
  await log.getByText("Saved your move.", { exact: true }).waitFor();
  // The provider is still held open: both the current chat and another open
  // view must see the committed memory before the reply finishes.
  for (const target of [page, observer.page]) {
    await target.waitForFunction(() =>
      window.Alpine.$data(document.body).records.some(
        (record) => record.title === "Move to the island on 12 June 2001",
      ),
    );
    await target
      .getByRole("searchbox", { name: "Search graph", exact: true })
      .fill("island on 12 June 2001");
    await target.locator("#graph-matches button").first().waitFor();
  }
  assert.equal(
    await page.evaluate(
      () => window.Alpine.$data(document.body).chatState.status,
    ),
    "running",
  );
  assert.ok(
    failedGraphRefresh && failedListRefresh,
    "A transient refresh failure was retried in the other view",
  );
  await page.waitForFunction(() => {
    const el = document.querySelector(".chat-log");
    return el.scrollHeight - el.clientHeight - el.scrollTop < 10;
  });
  await log.evaluate((el) => {
    el.scrollTop = 0;
  });
  await page
    .getByRole("button", { name: "Jump to latest", exact: true })
    .waitFor();
  next();
  await log.getByText("More details arrived.", { exact: true }).waitFor();
  assert.equal(await log.evaluate((el) => el.scrollTop), 0);
  await page
    .getByRole("button", { name: "Jump to latest", exact: true })
    .click();
  release();
  await page.getByText("Reply complete.", { exact: true }).waitFor();
  assert.equal(await log.locator("img").count(), 0);
  assert.equal(await page.evaluate(() => window.chatInjected), undefined);
  const state = await page.evaluate(
    () => window.Alpine.$data(document.body).chatState,
  );
  assert.equal(state.messages.filter((m) => m.role === "user").length, 1);
  assert.equal(state.messages.filter((m) => m.role === "tool").length, 2);
  const catalog = await (
    await fetch(url + "/api/records?user=browser-chat-person")
  ).json();
  // The saved record is inspected through the same public library API.
  const graph = await (await fetch(url + "/api/graph")).json();
  const node = graph.nodes.find(
    (n) => n.user === "browser-chat-person" && n.kind === "memories",
  );
  assert.ok(node, JSON.stringify(catalog));
  const record = await (await fetch(url + "/api/records/" + node.id)).json();
  assert.match(record.source, /2001-06-12/);
  await page.reload();
  await page.getByText("Reply complete.", { exact: true }).waitFor();
  assert.equal(
    await page.getByLabel("Talking as", { exact: true }).inputValue(),
    "browser-chat-person",
  );
  await page
    .getByRole("button", { name: "Conversations", exact: true })
    .click();
  assert.equal(
    await page
      .getByRole("button", {
        name: "Open conversation: " + state.title,
        exact: true,
      })
      .getAttribute("aria-current"),
    "page",
  );
  assert.equal(provider.calls(), 3);
  await page
    .getByRole("button", { name: "Close conversations", exact: true })
    .click();
  await page
    .locator("#chat-panel")
    .screenshot({ path: "/tmp/songya-ui-review/chat-desktop.png" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page
    .locator("#chat-panel")
    .screenshot({ path: "/tmp/songya-ui-review/chat-phone.png" });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  await page
    .getByRole("button", { name: "Chat settings", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .screenshot({ path: "/tmp/songya-ui-review/chat-settings-phone.png" });
  await page
    .getByRole("dialog")
    .getByLabel("Temperature", { exact: true })
    .fill("0");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Save connection", exact: true })
    .click();
  await page.reload();
  await page
    .getByRole("button", { name: "Chat settings", exact: true })
    .click();
  assert.equal(
    await page
      .getByRole("dialog")
      .getByLabel("Temperature", { exact: true })
      .inputValue(),
    "0",
  );
  await page.keyboard.press("Escape");
  await page.locator("#list-panel").scrollIntoViewIfNeeded();
  await page.getByRole("heading", { name: "Library", exact: true }).waitFor();
  assert.deepEqual(errors, []);
});

test(
  "chat queues rapid sends, keeps focus and drafts, and recovers a lost acknowledgement on reload",
  { timeout: 30000 },
  async (t) => {
    let allowAck, allowFirst, allowSecond, started;
    const ack = new Promise((done) => {
      allowAck = done;
    });
    const first = new Promise((done) => {
      allowFirst = done;
    });
    const second = new Promise((done) => {
      allowSecond = done;
    });
    const ready = new Promise((done) => {
      started = done;
    });
    t.after(() => {
      allowAck();
      allowFirst();
      allowSecond();
    });
    const provider = await chatProvider(t, async (res, body, n) => {
      streamChunk(res, { role: "assistant", content: `Queue reply ${n}` });
      if (n === 1) {
        started();
        await first;
      }
      if (n === 2) await second;
      streamFinish(res);
    });
    const { page, context } = await pageFor(undefined, undefined, "chat");
    t.after(() => context.close());
    await setupBrowserChat(page, provider.base, "browser-queue-person");
    let intercepted = false;
    await page.route("**/api/chat", async (route) => {
      if (intercepted) return route.continue();
      intercepted = true;
      const response = await route.fetch();
      assert.equal(response.status(), 202);
      await ack;
      await route.abort("failed");
    });
    const input = page.getByLabel("Message Songya", { exact: true });
    await input.fill("First message");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await ready;
    assert.equal(await input.isEnabled(), true);
    assert.equal(
      await input.evaluate((el) => document.activeElement === el),
      true,
    );
    assert.equal(await input.inputValue(), "");
    await input.fill("Second message");
    await input.press("Enter");
    assert.equal(await input.inputValue(), "");
    assert.equal(
      await input.evaluate((el) => document.activeElement === el),
      true,
    );
    await input.fill("Third message");
    await page
      .getByRole("button", { name: "Queue message", exact: true })
      .click();
    assert.equal(
      await input.evaluate((el) => document.activeElement === el),
      true,
    );
    await input.fill("An unsent fourth draft");
    assert.equal(
      await page.evaluate(
        () =>
          JSON.parse(localStorage.getItem("songya-chat-pending")).next.length,
      ),
      2,
    );
    allowAck();
    await page
      .getByRole("alert")
      .getByText(/Retry sends the same request/)
      .waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "Retry pending messages", exact: true })
        .isVisible(),
      true,
    );
    assert.equal(await input.inputValue(), "An unsent fourth draft");
    await page.reload();
    await page.waitForFunction(
      () => window.Alpine?.$data(document.body).chatState?.queue?.length === 2,
    );
    assert.equal(await input.inputValue(), "An unsent fourth draft");
    assert.equal(provider.calls(), 1);
    assert.equal(
      await page
        .getByRole("log")
        .getByText("· Queued", { exact: true })
        .count(),
      2,
    );
    allowFirst();
    await page
      .getByRole("log")
      .getByText("Queue reply 2", { exact: true })
      .waitFor();
    assert.equal(await input.isEnabled(), true);
    allowSecond();
    await page.getByText("Reply complete.", { exact: true }).waitFor();
    const state = await page.evaluate(
      () => window.Alpine.$data(document.body).chatState,
    );
    assert.deepEqual(
      state.messages.filter((m) => m.role === "user").map((m) => m.content),
      ["First message", "Second message", "Third message"],
    );
    assert.deepEqual(
      state.messages
        .filter((m) => m.role === "assistant")
        .map((m) => m.content),
      ["Queue reply 1", "Queue reply 2", "Queue reply 3"],
    );
    assert.equal(state.queue?.length || 0, 0);
    assert.equal(provider.calls(), 3);
    assert.equal(await input.inputValue(), "An unsent fourth draft");
    assert.deepEqual(errors, []);
  },
);

test("chat edits rewind and resend, preserve the composer, and conversations can be deleted", async (t) => {
  const requests = [];
  const provider = await chatProvider(t, async (res, body) => {
    const users = body.messages
      .filter((m) => m.role === "user")
      .map((m) => m.content);
    requests.push(users);
    streamChunk(res, { role: "assistant", content: "Reply: " + users.at(-1) });
    streamFinish(res);
  });
  const { page, context } = await pageFor(undefined, undefined, "chat");
  t.after(() => context.close());
  await setupBrowserChat(page, provider.base, "history-editor");
  const input = page.getByLabel("Message Songya", { exact: true });
  for (const text of ["Wrong words", "Later message"]) {
    await input.fill(text);
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await page
      .getByRole("log", { name: "Chat messages" })
      .getByText("Reply: " + text, { exact: true })
      .waitFor();
    await page.getByText("Reply complete.", { exact: true }).waitFor();
  }
  const id = await page.evaluate(
    () => window.Alpine.$data(document.body).chatSessionID,
  );
  await input.fill("Keep my next draft");
  const edit = page
    .getByRole("button", { name: "Edit message", exact: true })
    .first();
  await edit.click();
  const dialog = page.getByRole("dialog", {
    name: "Edit message",
    exact: true,
  });
  assert.equal(
    await dialog.getByLabel("Corrected message").inputValue(),
    "Wrong words",
  );
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  assert.equal(await page.locator(".chat-message.user").count(), 2);
  await edit.click();
  await dialog.getByLabel("Corrected message").fill("Correct words");
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth));
  let lostEditReply = false;
  await page.route("**/api/chat", async (route) => {
    if (!lostEditReply && route.request().postDataJSON()?.edit_turn_id) {
      lostEditReply = true;
      await route.fetch();
      await route.abort();
    } else await route.continue();
  });
  await dialog
    .getByRole("button", { name: "Save and resend", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Retry pending messages", exact: true })
    .click();
  await page
    .getByRole("log", { name: "Chat messages" })
    .getByText("Reply: Correct words", { exact: true })
    .waitFor();
  await page.getByText("Reply complete.", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "Keep my next draft");
  assert.equal(await page.locator(".chat-message.user").count(), 1);
  assert.equal(requests.at(-1).at(-1), "Correct words");
  assert.ok(
    requests
      .at(-1)
      .every((text) => !["Wrong words", "Later message"].includes(text)),
  );
  await page.reload();
  await page
    .getByRole("log", { name: "Chat messages" })
    .getByText("Reply: Correct words", { exact: true })
    .waitFor();
  await page.getByLabel("Conversation actions", { exact: true }).click();
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Delete conversation", exact: true })
    .click();
  assert.equal((await fetch(url + "/api/chat/sessions/" + id)).status, 200);
  let lostDeleteReply = false;
  await page.route("**/api/chat/sessions/" + id, async (route) => {
    if (!lostDeleteReply && route.request().method() === "DELETE") {
      lostDeleteReply = true;
      await route.fetch();
      await route.abort();
    } else await route.continue();
  });
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Delete conversation", exact: true })
    .click();
  await page.waitForFunction(
    () => !window.Alpine.$data(document.body).chatSessionID,
  );
  assert.equal((await fetch(url + "/api/chat/sessions/" + id)).status, 404);
  await page.reload();
  assert.equal(await page.locator(".chat-message").count(), 0);
  assert.deepEqual(errors, []);
});

test("chat lost acknowledgements replay one accepted message and truncated streams retain a retry", async (t) => {
  const provider = await chatProvider(t, async (res, body, n) => {
    streamChunk(res, { role: "assistant", content: "Partial response" });
    if (n === 1) res.end();
    else {
      streamChunk(res, { content: " recovered." });
      streamFinish(res);
    }
  });
  const { page, context } = await pageFor(undefined, undefined, "chat");
  t.after(() => context.close());
  await setupBrowserChat(page, provider.base, "browser-retry-person");
  let intercepted = false;
  await page.route("**/api/chat", async (route) => {
    if (intercepted) return route.continue();
    intercepted = true;
    const response = await route.fetch();
    assert.equal(response.status(), 202);
    await route.abort("failed");
  });
  const input = page.getByLabel("Message Songya", { exact: true });
  await input.fill("Keep my accepted message.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.getByRole("button", { name: "Retry send", exact: true }).waitFor();
  assert.equal(await input.inputValue(), "");
  await page
    .getByRole("log")
    .getByText("Keep my accepted message.", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Retry send", exact: true }).click();
  await page
    .getByRole("button", { name: "Continue saved turn", exact: true })
    .waitFor();
  await page
    .getByRole("log")
    .getByText("Partial response", { exact: true })
    .waitFor();
  assert.equal(provider.calls(), 1);
  await page
    .getByRole("button", { name: "Continue saved turn", exact: true })
    .click();
  await page.getByText("Reply complete.", { exact: true }).waitFor();
  const state = await page.evaluate(
    () => window.Alpine.$data(document.body).chatState,
  );
  assert.equal(state.messages.filter((m) => m.role === "user").length, 1);
  assert.equal(state.messages.at(-1).content, "Partial response recovered.");
  assert.equal(provider.calls(), 2);
  assert.deepEqual(errors, []);
});

test(
  "conversation history preserves separate drafts, rename and archive, and opens as a native drawer when it wraps",
  { timeout: 30000 },
  async (t) => {
    const provider = await chatProvider(t, async (res, body) => {
      streamChunk(res, {
        role: "assistant",
        content: "I remember: " + body.messages.at(-1).content,
      });
      streamFinish(res);
    });
    const { page, context } = await pageFor(
      { width: 1920, height: 900 },
      undefined,
      "chat",
    );
    t.after(() => context.close());
    await setupBrowserChat(page, provider.base, "history-person");
    const input = page.getByLabel("Message Songya", { exact: true });
    const current = () =>
      page.evaluate(() => window.Alpine.$data(document.body).chatSessionID);
    await input.fill("Summer on the island");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await page.getByText("Reply complete.", { exact: true }).waitFor();
    const first = await current();
    await input.fill("Draft for the island conversation");
    await page.getByRole("button", { name: "New chat", exact: true }).click();
    assert.equal(await input.inputValue(), "");
    await input.fill("Winter in the city");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await page.getByText("Reply complete.", { exact: true }).waitFor();
    const second = await current();
    assert.notEqual(first, second);
    await input.fill("Draft for the city conversation");
    await page
      .getByRole("button", {
        name: "Open conversation: Summer on the island",
        exact: true,
      })
      .click();
    assert.equal(await input.inputValue(), "Draft for the island conversation");
    await page.getByRole("button", { name: "New chat", exact: true }).click();
    await input.fill("An unsent new conversation");
    await page
      .getByRole("button", {
        name: "Open conversation: Winter in the city",
        exact: true,
      })
      .click();
    assert.equal(await input.inputValue(), "Draft for the city conversation");
    await page.reload();
    await page.getByText("Reply complete.", { exact: true }).waitFor();
    assert.equal(await current(), second);
    assert.equal(await input.inputValue(), "Draft for the city conversation");
    await page
      .getByRole("button", {
        name: "Open conversation: Summer on the island",
        exact: true,
      })
      .click();
    await page.getByLabel("Conversation actions", { exact: true }).click();
    await page
      .getByRole("button", { name: "Rename conversation", exact: true })
      .click();
    await page
      .getByLabel("Conversation name", { exact: true })
      .fill("Island summers");
    await page.getByRole("button", { name: "Save name", exact: true }).click();
    await page
      .getByRole("heading", { name: "Island summers", exact: true })
      .waitFor();
    assert.equal(await input.inputValue(), "Draft for the island conversation");
    await page
      .getByLabel("Search conversations", { exact: true })
      .fill("Winter");
    assert.equal(
      await page
        .getByRole("button", {
          name: "Open conversation: Island summers",
          exact: true,
        })
        .count(),
      0,
    );
    await page
      .getByRole("button", {
        name: "Open conversation: Winter in the city",
        exact: true,
      })
      .waitFor();
    await page.getByLabel("Search conversations", { exact: true }).fill("");
    await page.getByLabel("Conversation actions", { exact: true }).click();
    await page
      .getByRole("button", { name: "Archive conversation", exact: true })
      .click();
    await page.getByText("Archived conversation", { exact: true }).waitFor();
    assert.equal(await input.isDisabled(), true);
    await page.getByLabel("Conversation actions", { exact: true }).click();
    await page
      .getByRole("button", { name: "Restore conversation", exact: true })
      .click();
    await page
      .getByText("Archived conversation", { exact: true })
      .waitFor({ state: "hidden" });
    assert.equal(await input.isEnabled(), true);
    assert.equal(await input.inputValue(), "Draft for the island conversation");
    await page
      .locator("#chat-panel")
      .screenshot({ path: "/tmp/songya-ui-review/sessions-desktop-light.png" });
    await page.getByLabel("Color theme", { exact: true }).selectOption("dark");
    await page
      .locator("#chat-panel")
      .screenshot({ path: "/tmp/songya-ui-review/sessions-desktop-dark.png" });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForFunction(
      () => window.Alpine.$data(document.body).chatCompact,
    );
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    const drawer = page.getByRole("dialog", {
      name: "Conversations",
      exact: true,
    });
    assert.equal(await drawer.evaluate((el) => el.matches(":modal")), true);
    await drawer.screenshot({
      path: "/tmp/songya-ui-review/sessions-phone-drawer.png",
    });
    await drawer
      .getByRole("button", {
        name: "Open conversation: Winter in the city",
        exact: true,
      })
      .click();
    await drawer.waitFor({ state: "hidden" });
    assert.equal(await input.inputValue(), "Draft for the city conversation");
    await page
      .locator("#chat-panel")
      .screenshot({ path: "/tmp/songya-ui-review/sessions-phone-dark.png" });
    await page
      .getByRole("button", { name: "Expand chat", exact: true })
      .click();
    const box = await page.locator("#chat-panel").boundingBox();
    assert.equal(box.width, 390);
    assert.equal(box.height, 844);
    await page
      .getByRole("button", { name: "Conversations", exact: true })
      .click();
    await page.keyboard.press("Escape");
    await drawer.waitFor({ state: "hidden" });
    await page
      .getByRole("button", { name: "Exit expanded chat", exact: true })
      .waitFor();
    await page.keyboard.press("Escape");
    await page
      .getByRole("button", { name: "Expand chat", exact: true })
      .waitFor();
    await page.getByRole("button", { name: "New chat", exact: true }).click();
    assert.equal(await input.inputValue(), "An unsent new conversation");
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      true,
    );
    const stored = await (
      await fetch(url + "/api/chat/sessions/" + first)
    ).json();
    assert.equal(stored.title, "Island summers");
    assert.equal(stored.messages.filter((m) => m.role === "user").length, 1);
    assert.deepEqual(errors, []);
  },
);

test("color theme follows the device by default and persists explicit choices", async (t) => {
  const { page, context } = await pageFor(undefined, undefined, "chat");
  t.after(() => context.close());
  const theme = page.getByLabel("Color theme", { exact: true });
  const background = () =>
    page.evaluate(
      () => getComputedStyle(document.documentElement).backgroundColor,
    );
  assert.equal(await theme.inputValue(), "system");
  await page.emulateMedia({ colorScheme: "dark" });
  assert.equal(await background(), "rgb(17, 17, 27)");
  await theme.selectOption("light");
  assert.equal(await background(), "rgb(220, 224, 232)");
  await page.reload();
  assert.equal(await theme.inputValue(), "light");
  assert.equal(await background(), "rgb(220, 224, 232)");
  await theme.selectOption("system");
  assert.equal(await background(), "rgb(17, 17, 27)");
  await page.emulateMedia({ colorScheme: "light" });
  assert.equal(await background(), "rgb(220, 224, 232)");
  await theme.selectOption("dark");
  assert.equal(await background(), "rgb(17, 17, 27)");
  await page.locator("#list-panel").scrollIntoViewIfNeeded();
  assert.equal(
    await page.locator("main").evaluate((el) => getComputedStyle(el).color),
    "rgb(205, 214, 244)",
  );
  assert.deepEqual(errors, []);
});

test("retrieval settings persist on desktop and phone and test the actual saved graph policy", async (t) => {
  const entity = await save(
    source("needle-semantic musician", "needle-semantic biography"),
    "entities",
  );
  const memory = await save(
    source(
      "Connected rehearsal",
      "A quiet afternoon.",
      `relations: [{type: related_to, target: ${entity.id}}]\n`,
    ),
  );
  const { page, context } = await pageFor(undefined, undefined, "chat");
  t.after(() => context.close());
  await page
    .getByRole("button", { name: "Retrieval settings", exact: true })
    .click();
  const dialog = page.getByRole("dialog", {
    name: "Memory retrieval",
    exact: true,
  });
  await dialog.getByLabel("Maximum direct matches", { exact: true }).fill("1");
  await dialog.getByLabel("Minimum similarity", { exact: true }).fill("0.9");
  for (const kind of ["memories", "stories", "topics"])
    await dialog
      .getByRole("group", { name: "Search record types", exact: true })
      .getByLabel(kind, { exact: true })
      .uncheck();
  for (const kind of ["entities", "stories", "topics"])
    await dialog
      .getByRole("group", { name: "Return record types", exact: true })
      .getByLabel(kind, { exact: true })
      .uncheck();
  await dialog
    .getByLabel("Maximum connection depth", { exact: true })
    .fill("2");
  await dialog
    .getByLabel("Maximum connected records", { exact: true })
    .fill("3");
  await dialog
    .getByRole("button", { name: "Save retrieval settings", exact: true })
    .click();
  await dialog
    .getByText("Retrieval settings saved. New requests use this policy.", {
      exact: true,
    })
    .waitFor();
  await dialog
    .getByLabel("Retrieval message", { exact: true })
    .fill("needle-semantic");
  await dialog
    .getByLabel("Retrieval owner", { exact: true })
    .selectOption("ari");
  await dialog
    .getByRole("button", { name: "Find related memories", exact: true })
    .click();
  await dialog
    .locator(".recall-results")
    .getByText("Connected rehearsal", { exact: true })
    .waitFor();
  const result = await page.evaluate(
    () => window.Alpine.$data(document.body).recallResult,
  );
  assert.equal(result.items.length, 1);
  assert.equal(result.items[0].id, memory.id);
  assert.equal(result.items[0].reason, "connection");
  assert.equal(result.items[0].score, undefined);
  await page.setViewportSize({ width: 390, height: 844 });
  await dialog.screenshot({
    path: "/tmp/songya-ui-review/recall-settings-phone.png",
  });
  assert.equal(
    await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
    true,
  );
  await page.keyboard.press("Escape");
  await page.reload();
  await page
    .getByRole("button", { name: "Retrieval settings", exact: true })
    .click();
  assert.equal(
    await dialog
      .getByLabel("Maximum direct matches", { exact: true })
      .inputValue(),
    "1",
  );
  assert.equal(
    await dialog
      .getByLabel("Maximum connection depth", { exact: true })
      .inputValue(),
    "2",
  );
  assert.deepEqual(errors, []);
});

test("retrieval settings recover lost acknowledgements and retain conflicting drafts across reloads", async (t) => {
  const { page, context } = await pageFor(undefined, undefined, "chat");
  t.after(() => context.close());
  const open = () =>
    page
      .getByRole("button", { name: "Retrieval settings", exact: true })
      .click();
  const dialog = page.getByRole("dialog", {
    name: "Memory retrieval",
    exact: true,
  });
  await open();
  await dialog
    .getByLabel("Maximum connection depth", { exact: true })
    .fill("3");
  let writes = 0;
  await page.route("**/api/recall/settings", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    writes++;
    if (writes !== 1) return route.continue();
    const response = await route.fetch();
    assert.equal(response.status(), 200);
    await route.abort("failed");
  });
  await dialog
    .getByRole("button", { name: "Save retrieval settings", exact: true })
    .click();
  await dialog
    .getByRole("alert")
    .filter({ hasText: "Failed to fetch" })
    .waitFor();
  assert.equal(
    await dialog
      .getByLabel("Maximum connection depth", { exact: true })
      .inputValue(),
    "3",
  );
  await page.reload();
  await open();
  assert.equal(
    await dialog
      .getByLabel("Maximum connection depth", { exact: true })
      .inputValue(),
    "3",
  );
  assert.equal(writes, 1);
  await dialog.getByLabel("Maximum direct matches", { exact: true }).fill("7");
  const saved = await (await fetch(url + "/api/recall/settings")).json();
  saved.recall.connected_limit = 2;
  assert.equal(
    (
      await fetch(url + "/api/recall/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ version: saved.version, recall: saved.recall }),
      })
    ).status,
    200,
  );
  await dialog
    .getByRole("button", { name: "Save retrieval settings", exact: true })
    .click();
  await dialog.getByRole("alert").filter({ hasText: "changed" }).waitFor();
  await page.reload();
  await open();
  assert.equal(
    await dialog
      .getByLabel("Maximum direct matches", { exact: true })
      .inputValue(),
    "7",
  );
  await dialog
    .getByRole("button", { name: "Reload saved policy", exact: true })
    .click();
  await page.waitForFunction(
    (expected) =>
      window.Alpine.$data(document.body).recallDraft.direct_limit === expected,
    saved.recall.direct_limit,
  );
  assert.equal(
    await dialog
      .getByLabel("Maximum connected records", { exact: true })
      .inputValue(),
    "2",
  );
  assert.deepEqual(errors, []);
});

test("type settings preserve per-user choices, stable IDs and saved drafts", async () => {
  const { page, context } = await pageFor({ width: 390, height: 844 });
  try {
    await page
      .getByRole("button", { name: "Type settings", exact: true })
      .click();
    const dialog = page.locator('dialog[x-ref="typeSettingsDialog"]');
    await dialog.getByLabel("Type settings user").fill("cook");
    await dialog
      .getByRole("button", { name: "Load user", exact: true })
      .click();
    await dialog.getByLabel("Type name person", { exact: true }).waitFor();
    await dialog.getByRole("button", { name: "Add type", exact: true }).click();
    const added = dialog.locator(".type-definition").last();
    await added.locator("input:not([type=color])").fill("Ingredient");
    await added.locator("textarea").fill("An ingredient used for cooking.");
    await page.reload();
    await page
      .getByRole("button", { name: "Type settings", exact: true })
      .click();
    await dialog.getByLabel("Type settings user").fill("cook");
    await dialog
      .getByRole("button", { name: "Load user", exact: true })
      .click();
    await page.waitForFunction(
      () =>
        document.querySelector('[aria-label="Type name ingredient"]')?.value ===
        "Ingredient",
    );
    let savedOnce = false;
    const keys = [];
    await context.route("**/api/types/cook", async (route) => {
      if (route.request().method() !== "PUT") return route.continue();
      keys.push(route.request().postDataJSON().key);
      const response = await route.fetch();
      if (!savedOnce) {
        savedOnce = true;
        await route.abort("failed");
      } else await route.fulfill({ response });
    });
    await dialog
      .getByRole("button", { name: "Save types", exact: true })
      .click();
    await dialog
      .getByRole("button", { name: "Retry saved request", exact: true })
      .waitFor();
    await dialog
      .getByRole("button", { name: "Retry saved request", exact: true })
      .click();
    await dialog.getByText("Types saved for cook.", { exact: true }).waitFor();
    assert.equal(keys.length, 2);
    assert.equal(keys[0], keys[1]);
    await dialog
      .getByLabel("Type name ingredient", { exact: true })
      .fill("Cooking ingredient");
    await dialog
      .getByRole("button", { name: "Save types", exact: true })
      .click();
    await dialog.getByText("Types saved for cook.", { exact: true }).waitFor();
    const cook = await (await fetch(url + "/api/types/cook")).json();
    assert.equal(
      cook.kinds.entities.find((t) => t.id === "ingredient").name,
      "Cooking ingredient",
    );
    const life = await (await fetch(url + "/api/types/life")).json();
    assert.equal(life.kinds.entities.length, 12);
    assert.deepEqual(life.kinds.memories, []);
    const record = await save(
      "---\nname: Bulgur type test\ntypes: {cook: ingredient, life: food}\n---\nA shared food description.",
      "entities",
    );
    await dialog
      .getByRole("button", { name: "Close type settings", exact: true })
      .click();
    await page.reload();
    await page
      .getByRole("button", { name: "Type settings", exact: true })
      .click();
    await dialog.getByLabel("Type settings user").fill("cook");
    await dialog
      .getByRole("button", { name: "Load user", exact: true })
      .click();
    await dialog
      .getByRole("button", {
        name: "Remove type Cooking ingredient",
        exact: true,
      })
      .click();
    await dialog
      .getByLabel("Replace removed type Cooking ingredient", { exact: true })
      .selectOption("food");
    await dialog
      .getByRole("button", { name: "Save types", exact: true })
      .click();
    await dialog.getByText("Types saved for cook.", { exact: true }).waitFor();
    const after = await (await fetch(url + "/api/records/" + record.id)).json();
    assert.match(after.source, /cook: food/);
    assert.match(after.source, /life: food/);
    assert.match(after.source, /A shared food description\./);
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      true,
    );
    assert.equal(
      await dialog.evaluate(
        (el) => el.getBoundingClientRect().right <= innerWidth,
      ),
      true,
    );
    await page.screenshot({ path: "/tmp/songya-types-phone.png" });
    await dialog
      .getByRole("button", { name: "Close type settings", exact: true })
      .click();
    await page.locator("#list-panel").scrollIntoViewIfNeeded();
    await page.locator(".library-filters > summary").click();
    await page.getByLabel("Types for", { exact: true }).selectOption("cook");
    await editRecord(page, "Bulgur type test");
    const editor = page.locator('dialog[x-ref="editorDialog"]');
    await editor.getByRole("button", { name: "Metadata", exact: true }).click();
    await editor
      .getByLabel("Record classification", { exact: true })
      .selectOption("plant");
    await editor.getByRole("button", { name: "Save", exact: true }).click();
    await page.waitForFunction(
      () => window.Alpine.$data(document.body).dirty === false,
    );
    const edited = await (
      await fetch(url + "/api/records/" + record.id)
    ).json();
    assert.match(edited.source, /cook: plant/);
    assert.match(edited.source, /life: food/);
    assert.match(edited.source, /A shared food description\./);
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
  }
});

test("a background chat edit updates both views without replacing an unsaved editor draft", async (t) => {
  const record = await save(
    source("Live edit before", "John walked by the lake."),
  );
  await save(source("Live draft to keep", "An earlier account."));
  let allowWrite, finishReply;
  const write = new Promise((resolve) => {
    allowWrite = resolve;
  });
  const finish = new Promise((resolve) => {
    finishReply = resolve;
  });
  t.after(() => {
    allowWrite();
    finishReply();
  });
  const provider = await chatProvider(t, async (res, body, n) => {
    if (n === 1) {
      await write;
      return streamTool(res, "save_memory", {
        id: record.id,
        title: "Live edit after",
        text: "John walked by the lake with Mary.",
      });
    }
    streamChunk(res, {
      role: "assistant",
      content: "The correction is saved. Still replying.",
    });
    await finish;
    streamFinish(res);
  });
  const writer = await pageFor(undefined, undefined, "chat");
  const observer = await pageFor();
  t.after(() => writer.context.close());
  t.after(() => observer.context.close());
  await setupBrowserChat(writer.page, provider.base, "live-editor");
  await observer.page.locator(".library-filters > summary").click();
  await observer.page.getByLabel("Filter title", { exact: true }).fill("Live ");
  await observer.page
    .getByRole("button", { name: "Sort by Title", exact: true })
    .click();
  await editRecord(observer.page, "Live draft to keep");
  await observer.page
    .locator(".ProseMirror")
    .fill("My unsaved account must stay here.");
  await writer.page
    .getByLabel("Message Songya", { exact: true })
    .fill("Add Mary to John's account.");
  await writer.page.getByRole("button", { name: "Send", exact: true }).click();
  const id = await writer.page.evaluate(
    () => window.Alpine.$data(document.body).chatSessionID,
  );
  await writer.page
    .getByRole("button", { name: "New chat", exact: true })
    .click();
  allowWrite();
  for (const target of [writer.page, observer.page])
    await target.waitForFunction(
      (id) =>
        window.Alpine.$data(document.body).records.some(
          (r) => r.id === id && r.title === "Live edit after",
        ),
      record.id,
    );
  const state = await (await fetch(url + "/api/chat/sessions/" + id)).json();
  assert.equal(
    state.status,
    "running",
    "The update reaches the UI before the background reply completes",
  );
  assert.equal(
    await writer.page.evaluate(
      () => window.Alpine.$data(document.body).chatSessionID,
    ),
    "",
  );
  assert.equal(
    await observer.page.locator(".ProseMirror").innerText(),
    "My unsaved account must stay here.",
  );
  assert.equal(
    await observer.page.evaluate(
      () => window.Alpine.$data(document.body).dirty,
    ),
    true,
  );
  assert.deepEqual(
    await observer.page.evaluate(() => ({
      key: window.Alpine.$data(document.body).sortKey,
      ascending: window.Alpine.$data(document.body).ascending,
    })),
    { key: "title", ascending: true },
  );
  await observer.page
    .getByRole("button", { name: "Close record", exact: true })
    .click();
  assert.equal(
    await observer.page
      .getByLabel("Filter title", { exact: true })
      .inputValue(),
    "Live ",
  );
  await observer.page
    .getByRole("searchbox", { name: "Search graph", exact: true })
    .fill("Live edit after");
  await observer.page.locator("#graph-matches button").first().waitFor();
  finishReply();
  assert.deepEqual(errors, []);
});

test("last updated sorts real timestamps newest first and filters start collapsed", async () => {
  for (const [title, stamp] of [
    ["Updated oldest", "2020-01-01T00:00:00Z"],
    ["Updated newest", "2025-01-01T00:00:00Z"],
    ["Updated offset", "2025-01-01T02:00:00+03:00"],
    ["Updated unknown", "unreadable"],
  ]) {
    const record = await save(source(title));
    writeFileSync(
      join(root, "store", "memories", record.id + "/record.md"),
      record.source.replace(/updated_at: [^\n]+/, "updated_at: " + stamp),
    );
  }
  const { page, context } = await pageFor();
  try {
    const filters = page.locator(".library-filters");
    assert.equal(await filters.evaluate((el) => el.open), false);
    assert.equal(
      await page.getByLabel("Filter title", { exact: true }).isVisible(),
      false,
    );
    await filters.locator("summary").click();
    await page.getByLabel("Filter title", { exact: true }).fill("Updated ");
    const titles = () =>
      page.locator(".title-cell .text-button").allTextContents();
    assert.deepEqual(await titles(), [
      "Updated newest",
      "Updated offset",
      "Updated oldest",
      "Updated unknown",
    ]);
    const column = page.getByRole("columnheader").filter({
      has: page.getByRole("button", {
        name: "Sort by Last updated",
        exact: true,
      }),
    });
    assert.equal(await column.getAttribute("aria-sort"), "descending");
    const first = page.locator("tr.record").first().locator("time");
    assert.equal(await first.getAttribute("datetime"), "2025-01-01T00:00:00Z");
    assert.equal(
      await page.locator("tr.record").last().locator("time").innerText(),
      "Unknown",
    );
    await page
      .getByRole("button", { name: "Sort by Last updated", exact: true })
      .click();
    assert.deepEqual(await titles(), [
      "Updated oldest",
      "Updated offset",
      "Updated newest",
      "Updated unknown",
    ]);
    assert.equal(await column.getAttribute("aria-sort"), "ascending");
    await filters.locator("summary").click();
    assert.deepEqual(await titles(), [
      "Updated oldest",
      "Updated offset",
      "Updated newest",
      "Updated unknown",
    ]);
    await filters.locator("summary").click();
    assert.equal(
      await page.getByLabel("Filter title", { exact: true }).inputValue(),
      "Updated ",
    );
  } finally {
    await context.close();
  }
});

test("chat leads the intrinsic workspace and keeps scrolling and expansion independent", async () => {
  for (let i = 0; i < 20; i++)
    await save(
      source("Layout memory " + i, "A fictional afternoon with John."),
    );
  const { page, context } = await pageFor(
    { width: 1320, height: 900 },
    undefined,
    "chat",
  );
  try {
    assert.equal(
      await page.getByRole("tablist", { name: "Memory workspace" }).count(),
      0,
    );
    assert.deepEqual(
      await page
        .locator(".workspace > *")
        .evaluateAll((els) => els.map((el) => el.id || el.className)),
      ["chat-panel", "record-browser"],
    );
    const log = page.getByRole("log", { name: "Chat messages" });
    await log.evaluate((el) => {
      const account = document.createElement("p");
      account.textContent = "John remembered an afternoon by the lake. ".repeat(
        500,
      );
      el.append(account);
    });
    for (const width of [1920, 1320, 1000, 960, 940, 900, 390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate(
        () =>
          new Promise((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(resolve)),
          ),
      );
      const geometry = await page.evaluate(() => {
        const rect = (selector) => {
          const { x, y, width, height } = document
            .querySelector(selector)
            .getBoundingClientRect();
          return { x, y, width, height };
        };
        const style = getComputedStyle(document.querySelector(".workspace"));
        return {
          chat: rect("#chat-panel"),
          graph: rect(".memory-graph"),
          list: rect("#list-panel"),
          composer: rect(".chat-composer"),
          available:
            document.querySelector(".workspace").clientWidth -
            parseFloat(style.paddingLeft) -
            parseFloat(style.paddingRight),
          gap: parseFloat(style.columnGap),
          rem: parseFloat(getComputedStyle(document.documentElement).fontSize),
          overflow: document.documentElement.scrollWidth > innerWidth,
        };
      });
      const { chat, graph, list, composer, available, gap, rem } = geometry;
      if (available >= 64 * rem + gap) {
        assert.ok(
          chat.width >= 32 * rem && graph.width >= 32 * rem,
          "Each side-by-side column keeps its minimum reading width",
        );
        assert.ok(
          chat.x + chat.width <= graph.x && Math.abs(chat.y - graph.y) < 1,
          "Chat is left of the graph",
        );
      } else {
        assert.ok(
          chat.y + chat.height <= graph.y,
          "Chat comes before the stacked browser",
        );
        assert.ok(
          Math.abs(chat.width - available) < 1 &&
            Math.abs(graph.width - available) < 1,
          "Stacked columns fill the available width",
        );
      }
      assert.ok(
        graph.y + graph.height <= list.y,
        "The graph stays above the library",
      );
      assert.ok(
        Math.abs(graph.height - list.height) < 1,
        "Graph and library split the right column equally",
      );
      assert.ok(
        Math.abs(graph.x - list.x) < 1 &&
          Math.abs(graph.width - list.width) < 1,
        "The graph and library share one column",
      );
      assert.ok(
        composer.y >= chat.y &&
          composer.y + composer.height <= chat.y + chat.height + 1,
        "The composer stays inside the chat pane",
      );
      assert.equal(
        geometry.overflow,
        false,
        "The page does not scroll horizontally at " + width,
      );
      await log.evaluate((el) => {
        el.scrollTop = el.scrollHeight;
      });
      assert.ok(
        await log.evaluate((el) => el.scrollTop > 0),
        "Message history scrolls independently",
      );
      const table = page.getByRole("region", { name: "Records table" });
      await table.evaluate((el) => {
        el.scrollTop = el.scrollHeight;
        el.scrollLeft = 100;
      });
      assert.ok(
        await table.evaluate((el) => el.scrollTop > 0),
        "The record list scrolls independently",
      );
      assert.deepEqual(
        await page.locator(".chat-composer").boundingBox(),
        composer,
        "Scrolling either pane does not move the composer",
      );
      if (width === 1320 || width === 390)
        await page.screenshot({
          path: "/tmp/songya-ui-review/workspace-" + width + ".png",
          fullPage: true,
        });
    }
    await page
      .getByRole("button", { name: "Expand chat", exact: true })
      .click();
    assert.deepEqual(await page.locator("#chat-panel").boundingBox(), {
      x: 0,
      y: 0,
      width: 320,
      height: 900,
    });
    assert.equal(
      await page.locator(".record-browser").evaluate((el) => el.inert),
      true,
    );
    assert.equal(
      await page.locator("#chat-panel").evaluate((el) => el.inert),
      false,
    );
    await page.keyboard.press("Escape");
    assert.equal(
      await page.locator(".record-browser").evaluate((el) => el.inert),
      false,
    );
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
  }
});

test("entity lifetime dates use start/end and preserve content on desktop and phone", async () => {
  for (const width of [1320, 390]) {
    const title = "John lifetime " + width;
    const saved = await save(
      "---\nname: " + title + "\n---\nA short identification.",
      "entities",
    );
    const { page, context } = await pageFor({ width, height: 900 });
    try {
      await editRecord(page, title);
      const editor = page.locator('dialog[x-ref="editorDialog"]');
      await editor
        .getByRole("button", { name: "Metadata", exact: true })
        .click();
      await editor
        .getByRole("button", { name: "Add lifetime dates", exact: true })
        .click();
      await editor
        .getByLabel("Entity start date", { exact: true })
        .fill("1990-04-22");
      await editor
        .getByLabel("Entity end date", { exact: true })
        .fill("2070-06-01");
      assert.equal(
        await editor.getByLabel("Exact event date", { exact: true }).count(),
        0,
      );
      await editor.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(
        () => !window.Alpine.$data(document.body).dirty,
      );
      let record = await (await fetch(url + "/api/records/" + saved.id)).json();
      let timing = record.draft.fields.find(
        (field) => field.key === "time",
      ).time;
      assert.deepEqual(timing, { start: "1990-04-22", end: "2070-06-01" });
      assert.ok(record.source.endsWith("A short identification."));
      assert.doesNotMatch(record.source, /earliest:|latest:/);
      await editor.getByLabel("Entity end date", { exact: true }).fill("");
      await editor.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(
        () => !window.Alpine.$data(document.body).dirty,
      );
      record = await (await fetch(url + "/api/records/" + saved.id)).json();
      timing = record.draft.fields.find((field) => field.key === "time").time;
      assert.deepEqual(timing, { start: "1990-04-22", end: null });
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        true,
      );
      await page.screenshot({
        path: "/tmp/songya-entity-lifetime-" + width + ".png",
      });
    } finally {
      await context.close();
    }
  }
  assert.deepEqual(errors, []);
});

test("source editor syntax and controls remain readable in explicit and device themes", async () => {
  await save(
    source(
      "Source color palette",
      "# Notes about John\n\n**Strong words** and *gentle words*.\n\n[A link](https://example.invalid) and `inline code`.\n\nMary remembers an afternoon.",
      'message: "Hello Mary"\ncount: 42\nactive: true\nempty: null\n# A source comment\n',
    ),
  );
  const { page, context } = await pageFor();
  try {
    let draft;
    for (const [preference, device, expected] of [
      ["dark", "light", "dark"],
      ["light", "dark", "light"],
      ["system", "dark", "dark"],
      ["system", "light", "light"],
    ]) {
      await page.emulateMedia({ colorScheme: device });
      await page
        .getByLabel("Color theme", { exact: true })
        .selectOption(preference);
      await editRecord(page, "Source color palette");
      await page.getByRole("tab", { name: "Source", exact: true }).click();
      const content = page.locator(".cm-content");
      await content.waitFor({ state: "visible" });
      if (!draft) {
        await content.focus();
        await page.keyboard.press("ControlOrMeta+End");
        await page.keyboard.insertText(" Extra words by the lake.");
        draft = await content.innerText();
      }
      assert.equal(
        await content.innerText(),
        draft,
        "Theme changes preserve the unsaved source",
      );
      await content.focus();
      await page.keyboard.press("ControlOrMeta+a");
      await page.locator(".cm-selectionBackground").first().waitFor();
      const report = await page.evaluate(() => {
        const canvas = document.createElement("canvas"),
          ctx = canvas.getContext("2d");
        canvas.width = canvas.height = 1;
        const rgba = (color) => {
          ctx.clearRect(0, 0, 1, 1);
          ctx.fillStyle = color;
          ctx.fillRect(0, 0, 1, 1);
          return [...ctx.getImageData(0, 0, 1, 1).data].map((n) => n / 255);
        };
        const lum = (rgb) =>
          rgb
            .slice(0, 3)
            .map((c) =>
              c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4,
            )
            .reduce((sum, c, i) => sum + c * [0.2126, 0.7152, 0.0722][i], 0);
        const ratio = (a, b) => {
          const x = lum(a),
            y = lum(b);
          return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
        };
        const background = (el) => {
          const layers = [];
          for (let node = el; node; node = node.parentElement)
            layers.unshift(rgba(getComputedStyle(node).backgroundColor));
          return layers.reduce(
            (base, color) =>
              color.map((c, i) =>
                i === 3 ? 1 : c * color[3] + base[i] * (1 - color[3]),
              ),
            [1, 1, 1, 1],
          );
        };
        const selection = rgba(
          getComputedStyle(document.querySelector(".cm-selectionBackground"))
            .backgroundColor,
        );
        const samples = [
          document.querySelector(".cm-content"),
          ...document.querySelectorAll(".cm-content span"),
        ].filter((el) => el.textContent.trim());
        const colors = samples.map((el) => getComputedStyle(el).color);
        const failures = samples.flatMap((el) => {
          const color = rgba(getComputedStyle(el).color),
            normal = ratio(color, background(el)),
            selected = ratio(color, selection);
          return normal < 4.5 || selected < 4.5
            ? [
                {
                  text: el.textContent.slice(0, 36),
                  normal,
                  selected,
                  color: getComputedStyle(el).color,
                },
              ]
            : [];
        });
        const gutter = document.querySelector(".cm-gutters");
        return {
          failures,
          colors: [...new Set(colors)],
          background: getComputedStyle(document.querySelector(".cm-editor"))
            .backgroundColor,
          gutterContrast: ratio(
            rgba(getComputedStyle(gutter).color),
            background(gutter),
          ),
        };
      });
      assert.deepEqual(
        report.failures,
        [],
        JSON.stringify({ preference, device, failures: report.failures }),
      );
      assert.ok(
        report.colors.length >= 5,
        "YAML and Markdown retain distinct syntax colors",
      );
      assert.ok(report.gutterContrast >= 4.5, "Line numbers remain visible");
      assert.equal(
        report.background,
        expected === "dark" ? "rgb(17, 17, 27)" : "rgb(220, 224, 232)",
      );
      await page.keyboard.press("ArrowRight");
      assert.equal(
        await page
          .locator(".cm-cursor")
          .first()
          .evaluate((el) => getComputedStyle(el).borderLeftColor),
        expected === "dark" ? "rgb(205, 214, 244)" : "rgb(76, 79, 105)",
        "The drawn cursor remains visible",
      );
      if (preference !== "system")
        await page.locator('dialog[x-ref="editorDialog"]').screenshot({
          path: "/tmp/songya-ui-review/source-palette-" + expected + ".png",
        });
      await page.keyboard.press("ControlOrMeta+f");
      const search = page.locator(".cm-panel.cm-search");
      await search.waitFor();
      const surfaces = await search.evaluate((el) => ({
        panel: getComputedStyle(el.parentElement).backgroundColor,
        input: getComputedStyle(el.querySelector("input")).backgroundColor,
        foreground: getComputedStyle(el.querySelector("input")).color,
      }));
      assert.equal(
        surfaces.panel,
        expected === "dark" ? "rgb(24, 24, 37)" : "rgb(230, 233, 239)",
      );
      assert.equal(
        surfaces.input,
        expected === "dark" ? "rgb(30, 30, 46)" : "rgb(239, 241, 245)",
      );
      assert.equal(
        surfaces.foreground,
        expected === "dark" ? "rgb(205, 214, 244)" : "rgb(76, 79, 105)",
      );
      await search.locator('button[name="close"]').click();
      if (preference === "system" && device === "dark") {
        await content.focus();
        await page.keyboard.press("ControlOrMeta+End");
        await page.keyboard.insertText(" Extra words.");
        await page.emulateMedia({ colorScheme: "light" });
        await page.waitForFunction(
          () =>
            getComputedStyle(document.querySelector(".cm-editor"))
              .backgroundColor === "rgb(220, 224, 232)",
        );
        assert.match(await content.innerText(), /Extra words\./);
        await page.keyboard.press("ControlOrMeta+z");
        assert.equal(
          await content.innerText(),
          draft,
          "Device changes preserve undo history",
        );
      }
      await page
        .getByRole("button", { name: "Close record", exact: true })
        .click();
    }
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
  }
});

test("Catppuccin color presets and custom colors persist on network HTTP and recolor the graph", async () => {
  await fetch(url + "/api/types/palette_user");
  const entity = await save(
    "---\nname: Palette John\ntypes: {palette_user: person}\n---\nA fictional person.",
    "entities",
  );
  const memory = await save(
    source("Palette afternoon").replace("user: ari", "user: palette_user"),
  );
  const before = await (await fetch(url + "/api/records/" + entity.id)).json();
  const { page, context } = await pageFor(
    undefined,
    undefined,
    "list",
    url.replace("127.0.0.1", "songya.test"),
  );
  try {
    assert.deepEqual(
      await page.evaluate(() => ({
        secure: isSecureContext,
        uuid: typeof crypto.randomUUID,
        random: typeof crypto.getRandomValues,
      })),
      { secure: false, uuid: "undefined", random: "function" },
      "Exercise the same browser API restrictions as a network HTTP deployment",
    );
    await page.getByLabel("Color theme", { exact: true }).selectOption("dark");
    await page
      .getByLabel("Graph colors for", { exact: true })
      .selectOption("palette_user");
    await page
      .getByRole("button", { name: "Type settings", exact: true })
      .click();
    const dialog = page.locator('dialog[x-ref="typeSettingsDialog"]');
    await dialog
      .getByLabel("Type settings user", { exact: true })
      .fill("palette_user");
    await dialog
      .getByRole("button", { name: "Load user", exact: true })
      .click();
    const person = dialog.getByLabel("Color for type Person", { exact: true });
    await person.waitFor();
    const options = await person
      .locator("option")
      .evaluateAll((els) => els.map((el) => el.value));
    assert.equal(options.length, 24);
    assert.ok(
      !["crust", "mantle", "base"].some((key) => options.includes(key)),
    );
    assert.equal(await person.inputValue(), "mauve");
    assert.equal(
      await dialog
        .getByLabel("Color for memories", { exact: true })
        .inputValue(),
      "peach",
    );
    await person.selectOption("custom");
    const custom = dialog.getByLabel("Custom color for type person", {
      exact: true,
    });
    assert.equal(await custom.inputValue(), "#cba6f7");
    await custom.evaluate((el) => {
      el.value = "#e84d92";
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await dialog
      .getByLabel("Color for memories", { exact: true })
      .selectOption("blue");
    await dialog.screenshot({
      path: "/tmp/songya-ui-review/palette-settings-dark.png",
    });
    let writes = 0;
    await page.route("**/api/types/palette_user", async (route) => {
      if (route.request().method() !== "PUT") return route.continue();
      writes++;
      if (writes !== 1) return route.continue();
      // route.fetch runs in Node, outside Chromium's test DNS mapping.
      const response = await route.fetch({
        url: route.request().url().replace("songya.test", "127.0.0.1"),
        headers: {
          ...route.request().headers(),
          host: new URL(route.request().url()).host,
        },
      });
      assert.equal(response.status(), 200);
      await route.abort("failed");
    });
    await dialog
      .getByRole("button", { name: "Save types", exact: true })
      .click();
    await page.waitForFunction(
      () => !window.Alpine.$data(document.body).typeSettingsBusy,
    );
    assert.equal(
      writes,
      1,
      "The save must reach the server: " +
        (await dialog.getByRole("alert").textContent()),
    );
    await dialog
      .getByRole("alert")
      .filter({ hasText: "Failed to fetch" })
      .waitFor();
    await page.reload();
    await page.waitForFunction(
      () =>
        document.querySelector('[aria-label="Graph colors for"]').value ===
        "palette_user",
    );
    await page
      .getByRole("button", { name: "Type settings", exact: true })
      .click();
    await dialog
      .getByLabel("Type settings user", { exact: true })
      .fill("palette_user");
    await dialog
      .getByRole("button", { name: "Load user", exact: true })
      .click();
    await dialog
      .getByRole("button", { name: "Retry saved request", exact: true })
      .waitFor();
    assert.equal(await custom.inputValue(), "#e84d92");
    assert.equal(
      await dialog
        .getByLabel("Color for memories", { exact: true })
        .inputValue(),
      "blue",
    );
    await dialog
      .getByRole("button", { name: "Retry saved request", exact: true })
      .click();
    await dialog
      .getByText("Types saved for palette_user.", { exact: true })
      .waitFor();
    const saved = await (await fetch(url + "/api/types/palette_user")).json();
    assert.equal(saved.kind_colors.memories, "blue");
    assert.equal(
      saved.kinds.entities.find((t) => t.id === "person").color,
      "#e84d92",
    );
    const other = await (await fetch(url + "/api/types/palette_other")).json();
    assert.equal(other.kind_colors.memories, "peach");
    assert.equal(
      other.kinds.entities.find((t) => t.id === "person").color,
      "mauve",
    );
    assert.equal(
      (await (await fetch(url + "/api/records/" + entity.id)).json()).version,
      before.version,
    );
    await dialog
      .getByRole("button", { name: "Close type settings", exact: true })
      .click();
    await page.waitForFunction(
      (id) =>
        window.Alpine.$data(document.body).records.find((r) => r.id === id)
          ?.color === "#e84d92",
      entity.id,
    );
    // A node still moving in the initial force layout will leave the camera's
    // focus point. Settle it before sampling the focused node's center pixel.
    await page.waitForTimeout(3000);
    for (const [theme, background, memoryColor] of [
      ["dark", "rgb(17, 17, 27)", "rgb(137, 180, 250)"],
      ["light", "rgb(220, 224, 232)", "rgb(30, 102, 245)"],
    ]) {
      await page.getByLabel("Color theme", { exact: true }).selectOption(theme);
      assert.equal(
        await page.evaluate(
          () => getComputedStyle(document.documentElement).backgroundColor,
        ),
        background,
      );
      assert.equal(
        await page
          .locator('[data-kind="memories"]')
          .evaluate((el) => getComputedStyle(el).backgroundColor),
        memoryColor,
      );
      const row = page
        .locator("tr.record")
        .filter({ has: page.getByText(entity.id, { exact: true }) });
      assert.equal(
        await row
          .locator(".color-dot")
          .evaluate((el) => getComputedStyle(el).backgroundColor),
        "rgb(232, 77, 146)",
      );
      const graph = await (
        await fetch(url + "/api/graph?classification_user=palette_user")
      ).json();
      assert.equal(
        graph.nodes.find((n) => n.id === entity.id).color,
        "#e84d92",
      );
      assert.equal(graph.nodes.find((n) => n.id === memory.id).color, "blue");
      await page
        .getByRole("searchbox", { name: "Search graph", exact: true })
        .fill(entity.id);
      await page.locator("#graph-matches button").first().click();
      await page.waitForTimeout(800);
      const png = (
        await page.locator(".graph-canvas canvas").screenshot()
      ).toString("base64");
      const pixel = await page.evaluate(async (png) => {
        const image = new Image();
        image.src = "data:image/png;base64," + png;
        await image.decode();
        const canvas = document.createElement("canvas");
        canvas.width = image.width;
        canvas.height = image.height;
        const ctx = canvas.getContext("2d");
        ctx.drawImage(image, 0, 0);
        return [
          ...ctx.getImageData(
            Math.floor(image.width / 2),
            Math.floor(image.height / 2),
            1,
            1,
          ).data,
        ].slice(0, 3);
      }, png);
      assert.ok(
        pixel.every((value, i) => Math.abs(value - [232, 77, 146][i]) < 12),
        "The rendered node uses its saved custom color: " + pixel,
      );
      await page.screenshot({
        path: "/tmp/songya-ui-review/palette-workspace-" + theme + ".png",
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("button", { name: "Type settings", exact: true })
      .click();
    await dialog
      .getByLabel("Type settings user", { exact: true })
      .fill("palette_user");
    await dialog
      .getByRole("button", { name: "Load user", exact: true })
      .click();
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      true,
    );
    await dialog.screenshot({
      path: "/tmp/songya-ui-review/palette-settings-phone.png",
    });
    for (const name of ["Keepsake!", "Keepsake?"]) {
      await dialog
        .getByRole("button", { name: "Add type", exact: true })
        .click();
      const added = dialog.locator(".type-definition").last();
      await added.locator("input:not([type=color])").fill(name);
      await added.locator("textarea").fill("An object kept as a reminder.");
    }
    await dialog
      .getByRole("button", { name: "Save types", exact: true })
      .click();
    await dialog
      .getByText("Types saved for palette_user.", { exact: true })
      .waitFor();
    const edited = await (await fetch(url + "/api/types/palette_user")).json();
    const addedTypes = edited.kinds.entities.filter((type) =>
      type.name.startsWith("Keepsake"),
    );
    assert.equal(addedTypes.length, 2);
    assert.equal(new Set(addedTypes.map((type) => type.id)).size, 2);
    assert.ok(
      addedTypes.every((type) => /^keepsake(_[0-9a-f]{6})?$/.test(type.id)),
    );
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
  }
});

test("stories display composed memories with IDs, recover pages, and edit only their own source", async () => {
  const longText =
    "# A long afternoon\n\n" +
    "Mary remembered the lake. αβ. ".repeat(900) +
    "\n\nEnd of the long memory.";
  const first = await save(source("Reader first memory", longText));
  const second = await save(
    source(
      "Reader second memory",
      "John returned the next morning. <img src=x onerror=alert(1)>",
    ),
  );
  const nested = await save(
    "---\nuser: ari\ntitle: Reader nested story\nrelations:\n  - {type: related_to, target: " +
      second.id +
      "}\n  - {type: related_to, target: " +
      first.id +
      "}\n---\nNested introduction.",
    "stories",
  );
  const missing = "m_000000000000";
  const root = await save(
    "---\nuser: ari\ntitle: Reader complete story\nrelations:\n  - {type: related_to, target: " +
      first.id +
      "}\n  - {type: related_to, target: " +
      nested.id +
      "}\n  - {type: related_to, target: " +
      missing +
      "}\n---\nA story introduction.",
    "stories",
  );
  const initialFirst = await (
    await fetch(url + "/api/records/" + first.id)
  ).json();
  const initialSecond = await (
    await fetch(url + "/api/records/" + second.id)
  ).json();
  const { page, context } = await pageFor({ width: 1320, height: 900 });
  const dialog = page.locator('dialog[x-ref="editorDialog"]');
  let failedPage = false;
  try {
    await page.route(
      "**/api/stories/" + root.id + "/compose?offset=*",
      async (route) => {
        if (
          new URL(route.request().url()).searchParams.get("offset") !== "0" &&
          !failedPage
        ) {
          failedPage = true;
          return route.fulfill({
            status: 503,
            contentType: "application/json",
            body: JSON.stringify({ error: "Temporary story read failure" }),
          });
        }
        return route.continue();
      },
    );
    await page
      .getByRole("button", { name: "Reader complete story", exact: true })
      .click();
    const reader = dialog.getByRole("article", { name: "Composed story" });
    await reader
      .getByRole("alert")
      .getByText(/Temporary story read failure/)
      .waitFor();
    assert.match(await reader.textContent(), /A story introduction/);
    assert.match(await reader.textContent(), /Mary remembered/);
    await page.waitForFunction(
      () => window.Alpine.$data(document.body).storyComplete,
    );
    assert.equal(failedPage, true);
    assert.deepEqual(
      await reader.locator(".story-memory-id:visible").allTextContents(),
      [first.id, second.id, first.id, missing],
    );
    const passages = reader.locator(".story-part-body");
    assert.equal(
      await passages.filter({ hasText: "End of the long memory." }).count(),
      2,
    );
    assert.equal(
      await reader.locator("img").count(),
      0,
      "Saved HTML is displayed as text",
    );
    await reader
      .getByText("Story details needing attention", { exact: true })
      .click();
    assert.match(await reader.textContent(), /missing record/);
    assert.equal(
      await dialog.locator(".inspection-metadata").isVisible(),
      false,
    );
    for (const width of [1320, 390]) {
      await page.setViewportSize({ width, height: 900 });
      const part = reader
        .locator(".story-part")
        .filter({ hasText: first.id })
        .first();
      const idBounds = await part.locator(".story-memory-id").boundingBox();
      const textBounds = await part.locator(".story-part-body").boundingBox();
      assert.ok(
        idBounds.x + idBounds.width <= textBounds.x,
        "Memory ID stays to the left of its passage",
      );
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        true,
      );
      await dialog.evaluate((el) => {
        el.scrollTop = 0;
      });
      await dialog.screenshot({
        path: "/tmp/songya-ui-review/story-reader-" + width + ".png",
      });
    }
    await dialog.getByRole("tab", { name: "Source", exact: true }).click();
    const code = dialog.locator(".cm-content");
    await code.waitFor({ state: "visible" });
    assert.match(await code.innerText(), /A story introduction/);
    assert.doesNotMatch(await code.innerText(), /Mary remembered/);
    await dialog.getByRole("tab", { name: "Story", exact: true }).click();
    await dialog.getByRole("button", { name: "Edit", exact: true }).click();
    const editor = dialog.getByRole("textbox", {
      name: "Record text",
      exact: true,
    });
    await editor.waitFor();
    assert.equal((await editor.innerText()).trim(), "A story introduction.");
    await editor.fill("A revised introduction.");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await page.waitForFunction(() => !window.Alpine.$data(document.body).dirty);
    const updatedRoot = await (
      await fetch(url + "/api/records/" + root.id)
    ).json();
    assert.match(updatedRoot.source, /A revised introduction/);
    assert.doesNotMatch(updatedRoot.source, /Mary remembered/);
    for (const id of [first.id, nested.id, missing])
      assert.ok(updatedRoot.source.includes(id));
    assert.equal(
      (await (await fetch(url + "/api/records/" + first.id)).json()).version,
      initialFirst.version,
    );
    assert.equal(
      (await (await fetch(url + "/api/records/" + second.id)).json()).version,
      initialSecond.version,
    );
    assert.equal(
      (await (await fetch(url + "/api/records/" + nested.id)).json()).version,
      nested.version,
    );
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
  }
});

test("empty stories remain readable and closing a loading story cancels its pages", async () => {
  const empty = await save(source("Reader empty story", ""), "stories");
  const ordinary = await save(
    source("Reader ordinary memory", "John remembers a quiet morning."),
  );
  const { page, context } = await pageFor();
  const dialog = page.locator('dialog[x-ref="editorDialog"]');
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  try {
    await page
      .getByRole("button", { name: "Reader empty story", exact: true })
      .click();
    await dialog
      .getByText("This story has no text or linked memories yet.", {
        exact: true,
      })
      .waitFor();
    await dialog
      .getByRole("button", { name: "Close record", exact: true })
      .click();
    await page.route(
      "**/api/stories/" + empty.id + "/compose?offset=*",
      async (route) => {
        await gate;
        try {
          await route.abort();
        } catch {
          /* Closing the reader already cancelled it. */
        }
      },
    );
    const requested = page.waitForRequest((request) =>
      request.url().includes("/api/stories/" + empty.id + "/compose"),
    );
    await page
      .getByRole("button", { name: "Reader empty story", exact: true })
      .click();
    const request = await requested;
    const cancelled = page.waitForEvent("requestfailed", {
      predicate: (event) => event === request,
    });
    await dialog
      .getByRole("button", { name: "Close record", exact: true })
      .click();
    release();
    await cancelled;
    await page
      .getByRole("button", { name: "Reader ordinary memory", exact: true })
      .click();
    await dialog
      .getByRole("textbox", { name: "Record text", exact: true })
      .waitFor();
    assert.equal(
      (
        await dialog
          .getByRole("textbox", { name: "Record text", exact: true })
          .innerText()
      ).trim(),
      "John remembers a quiet morning.",
    );
    assert.equal(
      await dialog.getByRole("article", { name: "Composed story" }).isVisible(),
      false,
    );
    assert.deepEqual(
      await page.evaluate(() => window.Alpine.$data(document.body).storyParts),
      [],
    );
    assert.deepEqual(errors, []);
  } finally {
    release();
    await context.close();
  }
});

test("timeline shows uncertainty and lets dates and after links be corrected in the existing records", async () => {
  const subject = await save(
    "---\nname: Timeline case Mary\n---\nA fictional person.",
    "entities",
  );
  const add = (title, time = {}, relations = [], user = "ari") =>
    save(
      "---\nuser: " +
        user +
        "\ntitle: Timeline case " +
        title +
        "\ntime: " +
        JSON.stringify(time) +
        "\nrelations: " +
        JSON.stringify(relations) +
        "\n---\nJohn remembers " +
        title +
        ".",
    );
  const related = { type: "related_to", target: subject.id };
  const exact = await add("walk", { date: "2000-01-01" }, [related]);
  const range = await add(
    "journey",
    { earliest: "2001-01-01", latest: "2003-12-31" },
    [related],
  );
  const linked = await add("visit", {}, [
    related,
    { type: "after", target: exact.id },
  ]);
  const unknown = await add("unplaced");
  const relativeFirst = await add("first undated event");
  const relativeNext = await add("next undated event", {}, [
    { type: "after", target: relativeFirst.id },
  ]);
  const conflict = await add("conflicting date", { date: "1990-01-01" }, [
    { type: "after", target: exact.id },
  ]);
  const missing = await add("missing order target", {}, [
    { type: "after", target: "m_000000000000" },
  ]);
  const other = await add(
    "other person's memory",
    { date: "2005-01-01" },
    [],
    "timeline_other",
  );
  const originals = await Promise.all(
    [
      exact,
      range,
      linked,
      unknown,
      relativeFirst,
      relativeNext,
      conflict,
      missing,
      other,
    ].map(async (r) => (await fetch(url + "/api/records/" + r.id)).json()),
  );
  const { page, context } = await pageFor();
  const panel = page.getByRole("tabpanel", { name: "Timeline", exact: true });
  const dialog = page.locator('dialog[x-ref="editorDialog"]');
  const card = (id) => panel.locator('[data-memory-id="' + id + '"]');
  try {
    await page.locator(".library-filters > summary").click();
    await page
      .getByLabel("Search records", { exact: true })
      .fill("Timeline case");
    await page.waitForFunction(
      (id) =>
        window.Alpine.$data(document.body).records.some((r) => r.id === id),
      unknown.id,
    );
    await page.locator(".library-filters > summary").click();
    await page.getByRole("tab", { name: "Timeline", exact: true }).click();
    await card(unknown.id).waitFor();
    await page.waitForFunction(
      () => !window.Alpine.$data(document.body).timelineBusy,
    );
    assert.match(await card(range.id).textContent(), /2001-01-01 – 2003-12-31/);
    assert.match(await card(linked.id).textContent(), /Date unknown/);
    assert.match(
      await card(linked.id).textContent(),
      /Shown here using before\/after links/,
    );
    assert.ok(
      await panel
        .getByRole("region", {
          name: "Known order, dates unknown",
          exact: true,
        })
        .locator('[data-memory-id="' + relativeNext.id + '"]')
        .count(),
    );
    assert.match(await card(missing.id).textContent(), /unavailable record/);
    assert.match(await card(conflict.id).textContent(), /Timing conflict/);
    assert.ok((await card(exact.id).textContent()).includes(exact.id));
    for (const original of originals)
      assert.equal(
        (await (await fetch(url + "/api/records/" + original.id)).json())
          .version,
        original.version,
        "Reading the timeline must not alter memories",
      );
    await page.locator(".library-filters > summary").click();
    await page
      .getByLabel("Timeline timing", { exact: true })
      .selectOption("unknown");
    assert.ok(await card(unknown.id).count());
    assert.equal(await card(range.id).count(), 0);
    await page.getByLabel("Timeline timing", { exact: true }).selectOption("");
    await page
      .getByLabel("Timeline subject", { exact: true })
      .selectOption(subject.id);
    assert.deepEqual(
      new Set(
        await panel
          .locator(".timeline-entry")
          .evaluateAll((els) => els.map((el) => el.dataset.memoryId)),
      ),
      new Set([exact.id, range.id, linked.id]),
    );
    await page.getByLabel("Timeline subject", { exact: true }).selectOption("");
    await page.locator(".library-filters > summary").click();
    for (const [width, theme] of [
      [1320, "dark"],
      [390, "light"],
    ]) {
      await page.setViewportSize({ width, height: 900 });
      await page.getByLabel("Color theme", { exact: true }).selectOption(theme);
      await panel.scrollIntoViewIfNeeded();
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        true,
      );
      await page.screenshot({
        path: "/tmp/songya-ui-review/timeline-" + width + ".png",
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 1320, height: 900 });
    await card(unknown.id)
      .getByRole("button", {
        name: "Edit dates Timeline case unplaced",
        exact: true,
      })
      .click();
    await dialog
      .getByLabel("Exact event date", { exact: true })
      .fill("2002-06-15");
    await dialog
      .getByLabel("Exact event date", { exact: true })
      .dispatchEvent("change");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await page.waitForFunction(() => !window.Alpine.$data(document.body).dirty);
    await dialog
      .getByRole("button", { name: "Close record", exact: true })
      .click();
    await page.waitForFunction(
      (id) =>
        window.Alpine.$data(document.body).timelineRecords.find(
          (r) => r.id === id,
        )?.time.date === "2002-06-15",
      unknown.id,
    );
    assert.match(await card(unknown.id).textContent(), /2002-06-15/);
    await card(relativeFirst.id)
      .getByRole("button", {
        name: "Edit order Timeline case first undated event",
        exact: true,
      })
      .click();
    await dialog
      .getByLabel("Find a record", { exact: true })
      .fill("Timeline case unplaced");
    await dialog
      .locator(".target-results")
      .getByRole("button")
      .filter({ hasText: "Timeline case unplaced" })
      .click();
    assert.equal(
      await dialog.getByLabel("Relationship", { exact: true }).inputValue(),
      "after",
    );
    await dialog
      .getByRole("button", { name: "Add connection", exact: true })
      .click();
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await page.waitForFunction(() => !window.Alpine.$data(document.body).dirty);
    await dialog
      .getByRole("button", { name: "Close record", exact: true })
      .click();
    await page.waitForFunction(
      (id) =>
        window.Alpine.$data(document.body).timelineRecords.find(
          (r) => r.id === id,
        )?.section === "dated",
      relativeFirst.id,
    );
    assert.match(
      await card(relativeFirst.id).textContent(),
      /After Timeline case unplaced/,
    );
    const corrected = await (
      await fetch(url + "/api/records/" + relativeFirst.id)
    ).json();
    assert.match(corrected.source, new RegExp("target: " + unknown.id));
    assert.match(corrected.source, /type: after/);
    assert.equal(
      corrected.draft.body,
      originals.find((r) => r.id === relativeFirst.id).draft.body,
    );
    const latest = await (
      await fetch(url + "/api/records/" + linked.id)
    ).json();
    const changedSource = latest.source.replace(
      /^time:.*$/m,
      'time: {date: "2004-01-01", earliest: null, latest: null}',
    );
    assert.notEqual(
      changedSource,
      latest.source,
      "Fixture must update a real stored date field",
    );
    await save(changedSource, "memories", linked.id, latest.version);
    await page.waitForFunction(
      (id) =>
        window.Alpine.$data(document.body).timelineRecords.find(
          (r) => r.id === id,
        )?.time.date === "2004-01-01",
      linked.id,
    );
    let failTimeline = true;
    await page.route("**/api/timeline?*", async (route) => {
      if (failTimeline) {
        failTimeline = false;
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: "Temporary timeline failure" }),
        });
      }
      return route.continue();
    });
    await page.locator(".library-filters > summary").click();
    await page.getByLabel("Owner", { exact: true }).selectOption("ari");
    await panel
      .getByRole("alert")
      .getByText(/Temporary timeline failure/)
      .waitFor();
    assert.ok(
      await card(exact.id).isVisible(),
      "A failed refresh keeps healthy memories readable",
    );
    await page.waitForFunction(
      () =>
        !window.Alpine.$data(document.body).timelineError &&
        !window.Alpine.$data(document.body).timelineBusy,
    );
    assert.equal(
      await card(other.id).count(),
      0,
      "Owner changes exclude other collections",
    );
    await page.locator(".library-filters > summary").click();
    await page.getByRole("tab", { name: "List", exact: true }).click();
    await page.locator("#library-list").waitFor({ state: "visible" });
    assert.equal(await page.locator("#library-list").isVisible(), true);
    assert.equal(await panel.isVisible(), false);
    assert.deepEqual(errors, []);
  } finally {
    await context.close();
  }
});

test("date fields and calendars stay ISO in different locales and preserve edits", async () => {
  for (const [locale, width, theme] of [
    ["en-US", 1320, "dark"],
    ["de-DE", 390, "light"],
  ]) {
    const memory = await save(
      source(
        "ISO calendar " + locale,
        "John remembers the lake.",
        'time: {date: null, earliest: "1981-05-01", latest: "1981-06-30"}\n',
      ),
    );
    const { page, context } = await pageFor(
      { width, height: 900 },
      undefined,
      "list",
      url,
      { locale, timezoneId: "America/Los_Angeles" },
    );
    const dialog = page.locator('dialog[x-ref="editorDialog"]');
    let writes = 0;
    context.on("request", (request) => {
      if (request.method() === "POST" && request.url() === url + "/api/records")
        writes++;
    });
    try {
      await page.getByLabel("Color theme", { exact: true }).selectOption(theme);
      assert.equal(
        await page.evaluate(() =>
          window.Alpine.$data(document.body).updatedLabel(
            "2026-11-08T06:04:05Z",
          ),
        ),
        "2026-11-07 22:04:05",
      );
      assert.equal(
        await page.evaluate(() =>
          window.Alpine.$data(document.body).chatSessionDate({
            updated_at: Date.parse("2026-11-08T06:04:05Z"),
          }),
        ),
        "2026-11-07",
      );
      await editRecord(page, "ISO calendar " + locale);
      await dialog
        .getByRole("button", { name: "Metadata", exact: true })
        .click();
      const input = dialog.getByLabel("Exact event date", { exact: true });
      assert.equal(await input.getAttribute("type"), "text");
      assert.equal(await input.getAttribute("placeholder"), "YYYY-MM-DD");
      assert.equal(
        await dialog
          .getByLabel("Earliest event date", { exact: true })
          .inputValue(),
        "1981-05-01",
      );
      const opener = dialog.getByRole("button", {
        name: "Exact event date calendar",
        exact: true,
      });
      await opener.click();
      const calendar = page.getByRole("dialog", {
        name: "Exact event date calendar",
        exact: true,
      });
      await calendar.waitFor();
      await calendar
        .getByRole("spinbutton", { name: "Calendar year", exact: true })
        .fill("1983");
      await calendar
        .getByRole("spinbutton", { name: "Calendar year", exact: true })
        .dispatchEvent("change");
      await calendar
        .getByRole("combobox", { name: "Calendar month", exact: true })
        .selectOption("6");
      assert.equal(
        await calendar.locator(".iso-calendar-period").textContent(),
        "1983-07",
      );
      assert.equal(
        await page.evaluate(() => window.Alpine.$data(document.body).dirty),
        false,
        "Browsing the calendar must not change the memory",
      );
      const box = await calendar.boundingBox();
      assert.ok(
        box.x >= 0 &&
          box.y >= 0 &&
          box.x + box.width <= width &&
          box.y + box.height <= 900,
        "Calendar stays within the browser viewport",
      );
      await page.screenshot({
        path: "/tmp/songya-ui-review/iso-calendar-" + locale + ".png",
      });
      await calendar
        .getByRole("spinbutton", { name: "Calendar year", exact: true })
        .fill("");
      await calendar
        .getByRole("button", { name: "1983-07-13", exact: true })
        .click();
      assert.equal(
        await calendar.isVisible(),
        true,
        "An incomplete year must not select a date from the old year",
      );
      assert.equal(await input.inputValue(), "");
      await calendar
        .getByRole("spinbutton", { name: "Calendar year", exact: true })
        .fill("1983");
      await calendar
        .getByRole("spinbutton", { name: "Calendar year", exact: true })
        .dispatchEvent("change");
      await calendar
        .getByRole("button", { name: "1983-07-13", exact: true })
        .click();
      await calendar.waitFor({ state: "hidden" });
      assert.equal(await input.inputValue(), "1983-07-13");
      assert.equal(
        await dialog
          .getByLabel("Earliest event date", { exact: true })
          .inputValue(),
        "",
      );
      assert.equal(
        await dialog
          .getByLabel("Latest event date", { exact: true })
          .inputValue(),
        "",
      );
      await input.fill("1983-02-29");
      await input.dispatchEvent("change");
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      assert.equal(
        writes,
        0,
        "An impossible date is retained for correction without publishing it",
      );
      assert.equal(await input.inputValue(), "1983-02-29");
      assert.equal(await input.evaluate((el) => el.checkValidity()), false);
      await input.fill("19810501");
      await input.dispatchEvent("change");
      assert.equal(await input.inputValue(), "1981-05-01");
      await opener.click();
      await page.keyboard.press("Escape");
      await calendar.waitFor({ state: "hidden" });
      assert.equal(
        await dialog.isVisible(),
        true,
        "Escape closes the calendar before the record editor",
      );
      await input.fill("2024-02-29");
      await input.dispatchEvent("change");
      await opener.click();
      await page.keyboard.press("ArrowRight");
      assert.equal(
        await calendar.locator(".iso-calendar-period").textContent(),
        "2024-03",
      );
      await page.keyboard.press("Enter");
      await calendar.waitFor({ state: "hidden" });
      assert.equal(await input.inputValue(), "2024-03-01");
      await opener.click();
      await calendar
        .getByRole("button", { name: "Clear", exact: true })
        .click();
      assert.equal(await input.inputValue(), "");
      await input.fill("2024-02-29");
      await input.dispatchEvent("change");
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(
        () => !window.Alpine.$data(document.body).dirty,
      );
      const saved = await (
        await fetch(url + "/api/records/" + memory.id)
      ).json();
      assert.equal(
        saved.draft.fields.find((f) => f.key === "time").time.date,
        "2024-02-29",
      );
      assert.equal(saved.draft.body, "John remembers the lake.");
      if (locale === "en-US") {
        await input.fill("2024-02-30");
        await input.dispatchEvent("change");
        await dialog.getByRole("tab", { name: "Source", exact: true }).click();
        const code = dialog.locator(".cm-content");
        await code.waitFor({ state: "visible" });
        const source = await code.innerText();
        assert.ok(
          source.includes("2024-02-30"),
          "An invalid draft is preserved when switching editors",
        );
        await code.focus();
        await page.keyboard.press("ControlOrMeta+a");
        await page.keyboard.insertText(
          source.replace("2024-02-30", "2024-03-01"),
        );
        await dialog.getByRole("button", { name: "Save", exact: true }).click();
        await page.waitForFunction(
          () => !window.Alpine.$data(document.body).dirty,
        );
        const corrected = await (
          await fetch(url + "/api/records/" + memory.id)
        ).json();
        assert.equal(
          corrected.draft.fields.find((f) => f.key === "time").time.date,
          "2024-03-01",
        );
        assert.equal(corrected.draft.body, "John remembers the lake.");
      }
      await dialog
        .getByRole("button", { name: "Close record", exact: true })
        .click();
      const row = page
        .locator("tr.record")
        .filter({ has: page.getByText(memory.id, { exact: true }) });
      assert.match(
        await row.locator(".updated-cell time").textContent(),
        /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/,
      );
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        true,
      );
      assert.deepEqual(errors, []);
    } finally {
      await context.close();
    }
  }
});

test("record files upload without changing prose, preview safely, survive reload and remove explicitly", async () => {
  const record = await save(
    source("Mary's attached files", "Words to preserve."),
  );
  const { page, context } = await pageFor();
  await editRecord(page, "Mary's attached files");
  await page.locator(".record-files input[type=file][multiple]").setInputFiles([
    {
      name: "record.md",
      mimeType: "text/markdown",
      buffer: Buffer.from("A separate note"),
    },
    {
      name: "資料.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("A readable note"),
    },
    {
      name: "danger.html",
      mimeType: "text/html",
      buffer: Buffer.from("<script>window.uploadExecuted=true</script>"),
    },
  ]);
  await page.waitForFunction(
    () => document.querySelectorAll(".record-files .file-card").length === 3,
  );
  const listing = await (
    await fetch(url + "/api/records/" + record.id + "/files")
  ).json();
  assert.deepEqual(
    listing.files.map((f) => f.filename),
    ["danger.html", "record (file).md", "資料.txt"],
  );
  assert.equal(
    (await (await fetch(url + "/api/records/" + record.id)).json()).draft.body,
    "Words to preserve.",
  );
  await page
    .locator(".record-files .file-card")
    .filter({ hasText: "資料.txt" })
    .getByRole("button", { name: "Preview text" })
    .click();
  await page
    .locator(".record-files pre")
    .filter({ hasText: "A readable note" })
    .waitFor();
  assert.equal(await page.evaluate(() => window.uploadExecuted), undefined);
  const unsafe = await fetch(
    url + "/api/records/" + record.id + "/files/danger.html?preview=1",
  );
  assert.match(unsafe.headers.get("content-disposition"), /^attachment/);
  assert.equal(unsafe.headers.get("content-type"), "application/octet-stream");
  await page.keyboard.press("Escape");
  await page.reload();
  await editRecord(page, "Mary's attached files");
  assert.equal(await page.locator(".record-files .file-card").count(), 3);
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .locator(".record-files .file-card")
    .filter({ hasText: "資料.txt" })
    .getByRole("button", { name: "Remove permanently" })
    .click();
  await page.waitForFunction(
    () => document.querySelectorAll(".record-files .file-card").length === 2,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("new record files remain staged through rejected text saves and retry a lost attachment reply once", async () => {
  const { page, context } = await pageFor();
  await page.getByRole("button", { name: "New record", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Mary's durable upload");
  await page.locator(".record-files input[type=file][multiple]").setInputFiles({
    name: "letter.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("The complete letter"),
  });
  await page
    .locator(".record-files .upload-ready")
    .filter({ hasText: "letter.txt" })
    .waitFor();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page
    .getByRole("alert")
    .filter({ hasText: /user|owner/i })
    .first()
    .waitFor();
  assert.equal(await page.locator(".record-files .file-card").count(), 0);
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("john");
  let lost = false;
  await page.route("**/api/records/*/files", async (route) => {
    if (route.request().method() === "POST" && !lost) {
      lost = true;
      await route.fetch();
      return route.abort("failed");
    }
    return route.continue();
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page
    .locator(".record-files .error")
    .filter({ hasText: /fetch|failed|network/i })
    .waitFor();
  await page
    .getByRole("button", { name: "Attach uploaded files / retry", exact: true })
    .click();
  await page.waitForFunction(
    () => document.querySelectorAll(".record-files .file-card").length === 1,
  );
  await page.waitForFunction(
    () => document.querySelectorAll(".record-files .upload-ready").length === 0,
  );
  const records = (
    await (
      await fetch(
        url + "/api/records?q=" + encodeURIComponent("Mary's durable upload"),
      )
    ).json()
  ).records;
  assert.equal(
    records.filter((record) => record.title === "Mary's durable upload").length,
    1,
  );
  const savedRecord = records.find(
    (record) => record.title === "Mary's durable upload",
  );
  const files = await (
    await fetch(url + "/api/records/" + savedRecord.id + "/files")
  ).json();
  assert.equal(files.files.length, 1);
  await page.keyboard.press("Escape");
  await page.reload();
  await editRecord(page, "Mary's durable upload");
  assert.equal(await page.locator(".record-files .file-card").count(), 1);
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat uploads supply receipts to text-only tools and confirm attached files without changing testimony", async (t) => {
  let id, upload;
  const provider = await chatProvider(t, async (res, body, n) => {
    const user = body.messages.filter((m) => m.role === "user").at(-1).content;
    upload ||= user.match(/"upload":"([a-f0-9]{64})"/)?.[1];
    assert.ok(upload);
    if (n === 1)
      return streamTool(res, "prepare_memory", { text: "Mary's written note" });
    if (n === 2)
      return streamTool(res, "save_memory", {
        title: "Mary's written note",
        text: "Mary kept the letter.",
      });
    const data = JSON.parse(body.messages.at(-1).content).structuredContent
      .data;
    if (n === 3) {
      id = data.id;
      assert.ok(id);
      return streamTool(res, "list_files", { id });
    }
    if (n === 4)
      return streamTool(res, "attach_file", {
        id,
        revision: data.revision,
        upload,
      });
    assert.equal(data.status, "committed");
    streamChunk(res, {
      role: "assistant",
      content: "Saved the note and attached letter.txt.",
    });
    return streamFinish(res);
  });
  const { page, context } = await pageFor();
  await setupBrowserChat(page, provider.base, "john-files-chat");
  await page.getByLabel("Attach files", { exact: true }).setInputFiles({
    name: "letter.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Mary's full letter"),
  });
  await page
    .locator(".chat-attachments .chat-attachment")
    .filter({ hasText: "letter.txt" })
    .waitFor();
  await page
    .getByRole("textbox", { name: "Message Songya", exact: true })
    .fill("Keep Mary's letter with this memory.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page
    .locator(".chat-progress")
    .getByText("Reply complete.", { exact: true })
    .waitFor();
  await page
    .locator(".chat-message .upload-ready")
    .filter({ hasText: "Attached to " + id })
    .waitFor();
  const record = await (await fetch(url + "/api/records/" + id)).json();
  assert.match(record.source, /Keep Mary's letter with this memory\./);
  assert.doesNotMatch(record.source, /App upload receipts|not yet attached/);
  assert.equal(
    await (
      await fetch(url + "/api/records/" + id + "/files/letter.txt")
    ).text(),
    "Mary's full letter",
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("maximum-length duplicate filenames stay distinct and fit a phone's file cards", async () => {
  const record = await save(
    source("John's long filenames", "Preserve the account."),
  );
  const filename = "a".repeat(251) + ".txt";
  const { page, context } = await pageFor({ width: 390, height: 844 });
  await editRecord(page, "John's long filenames");
  await page.locator(".record-files input[type=file][multiple]").setInputFiles([
    {
      name: filename,
      mimeType: "text/plain",
      buffer: Buffer.from("First original file"),
    },
    {
      name: filename,
      mimeType: "text/plain",
      buffer: Buffer.from("Second original file"),
    },
  ]);
  await page.waitForFunction(
    () => document.querySelectorAll(".record-files .file-card").length === 2,
  );
  const files = (
    await (await fetch(url + "/api/records/" + record.id + "/files")).json()
  ).files;
  assert.equal(new Set(files.map((file) => file.filename)).size, 2);
  assert.ok(files.every((file) => Buffer.byteLength(file.filename) <= 255));
  assert.deepEqual(
    new Set(
      await Promise.all(
        files.map(async (file) =>
          (
            await fetch(
              url +
                "/api/records/" +
                record.id +
                "/files/" +
                encodeURIComponent(file.filename),
            )
          ).text(),
        ),
      ),
    ),
    new Set(["First original file", "Second original file"]),
  );
  const dialog = page.locator("dialog[x-ref=editorDialog]");
  await dialog.locator(".file-card").last().scrollIntoViewIfNeeded();
  await dialog.evaluate((element) => (element.scrollLeft = 0));
  const dimensions = await dialog.evaluate((element) => ({
    width: element.clientWidth,
    scroll: element.scrollWidth,
  }));
  assert.ok(
    dimensions.scroll <= dimensions.width + 1,
    JSON.stringify(dimensions),
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat sends a draft with its uploading files and keeps the next message separate", async (t) => {
  const received = [];
  const provider = await chatProvider(t, (res, body) => {
    received.push(
      body.messages.filter((m) => m.role === "user").at(-1).content,
    );
    streamChunk(res, { role: "assistant", content: "Received." });
    streamFinish(res);
  });
  const { page, context } = await pageFor({ width: 390, height: 844 });
  await setupBrowserChat(page, provider.base, "john-upload-wait");
  let release, uploading;
  const started = new Promise((resolve) => (uploading = resolve));
  const gate = new Promise((resolve) => (release = resolve));
  await page.route("**/api/uploads/*?*", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    uploading();
    await gate;
    await route.continue();
  });
  t.after(() => release());
  let posts = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/api/chat"
    )
      posts++;
  });
  await page.getByLabel("Attach files", { exact: true }).setInputFiles({
    name: "letter.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Mary's letter"),
  });
  await started;
  const input = page.getByRole("textbox", {
    name: "Message Songya",
    exact: true,
  });
  await input.fill("Keep this letter.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  assert.equal(await input.inputValue(), "");
  assert.equal(
    await input.evaluate((element) => document.activeElement === element),
    true,
  );
  assert.equal(await input.isEnabled(), true);
  await input.fill("My next message.");
  await input.press("Enter");
  assert.equal(posts, 0, "No message may overtake an unfinished attachment");
  assert.equal(
    await page.locator(".chat-attachments .chat-attachment").count(),
    0,
  );
  release();
  await page.waitForFunction(
    () => document.querySelectorAll(".chat-message.assistant").length === 2,
  );
  assert.equal(received.length, 2);
  assert.match(received[0], /Keep this letter\./);
  assert.match(received[0], /letter\.txt/);
  assert.equal(received[1], "My next message.");
  const sessions = await (
    await context.request.get(url + "/api/chat/sessions")
  ).json();
  const conversation = await (
    await context.request.get(
      url +
        "/api/chat/sessions/" +
        sessions.sessions.find((s) => s.user === "john-upload-wait").id,
    )
  ).json();
  const turns = conversation.messages.filter((m) => m.role === "user");
  assert.deepEqual(
    turns.map((m) => m.content),
    ["Keep this letter.", "My next message."],
  );
  assert.equal(turns[0].uploads.length, 1);
  assert.equal((turns[1].uploads || []).length, 0);
  assert.equal(
    await page
      .getByText("Uploaded files · choose which to include", { exact: true })
      .count(),
    0,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat draft attachments survive reload, can be removed, and send without message text", async (t) => {
  let received;
  const provider = await chatProvider(t, (res, body) => {
    received = body.messages.filter((m) => m.role === "user").at(-1).content;
    streamChunk(res, { role: "assistant", content: "Received the file." });
    streamFinish(res);
  });
  const { page, context } = await pageFor({ width: 390, height: 844 });
  await setupBrowserChat(page, provider.base, "mary-file-draft");
  const earlier = await context.request.put(
    url +
      "/api/uploads/earlier-draft?scope=chat:mary-file-draft&filename=earlier.txt",
    { data: "A file from another draft" },
  );
  assert.equal(earlier.status(), 200);
  assert.equal(await page.locator(".chat-attachment").count(), 0);
  const name = "a".repeat(200) + ".txt";
  await page.getByLabel("Attach files", { exact: true }).setInputFiles([
    { name, mimeType: "text/plain", buffer: Buffer.from("Keep this") },
    {
      name: "remove.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("Do not send"),
    },
  ]);
  await page.waitForFunction(() => {
    const state = window.Alpine.$data(document.querySelector("[x-data]"));
    return (
      state.chatAttachments.length === 2 &&
      state.chatAttachments.every((a) => a.status === "ready")
    );
  });
  await page
    .getByRole("button", { name: "Remove remove.txt", exact: true })
    .click();
  await page.reload();
  await page.locator(".chat-attachment").filter({ hasText: name }).waitFor();
  assert.equal(await page.locator(".chat-attachment").count(), 1);
  const composer = page.locator(".chat-composer");
  const dimensions = await composer.evaluate((element) => ({
    width: element.clientWidth,
    scroll: element.scrollWidth,
  }));
  assert.ok(
    dimensions.scroll <= dimensions.width + 1,
    JSON.stringify(dimensions),
  );
  await page.getByLabel("Color theme", { exact: true }).selectOption("dark");
  await page.screenshot({ path: "/tmp/songya-chat-attachments-dark.png" });
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page
    .locator(".chat-message.assistant")
    .getByText("Received the file.", { exact: true })
    .waitFor();
  assert.ok(received.includes(name));
  assert.doesNotMatch(received, /remove\.txt|earlier\.txt/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat recovers a lost upload reply and retries the same message after a lost send reply", async (t) => {
  const provider = await chatProvider(t, (res) => {
    streamChunk(res, { role: "assistant", content: "Received once." });
    streamFinish(res);
  });
  const { page, context } = await pageFor();
  await setupBrowserChat(page, provider.base, "john-lost-file-replies");
  let uploadReplies = 0;
  await page.route("**/api/uploads/*?*", async (route) => {
    if (route.request().method() !== "PUT" || uploadReplies++)
      return route.continue();
    await route.fetch();
    await route.abort();
  });
  const requests = [];
  await page.route("**/api/chat", async (route) => {
    requests.push(route.request().postDataJSON());
    if (requests.length > 1) return route.continue();
    await route.fetch();
    await route.abort();
  });
  await page.getByLabel("Attach files", { exact: true }).setInputFiles({
    name: "letter.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Keep these bytes"),
  });
  const input = page.getByRole("textbox", {
    name: "Message Songya",
    exact: true,
  });
  await input.fill("Keep the letter.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page
    .locator(".chat-error")
    .getByText(/Retry sends the same request/)
    .waitFor();
  await page.reload();
  await page
    .locator(".chat-message.assistant")
    .getByText("Received once.", { exact: true })
    .waitFor();
  await page.waitForFunction(
    () => localStorage.getItem("songya-chat-pending") === null,
  );
  assert.equal(requests.length, 2);
  assert.equal(requests[0].request_id, requests[1].request_id);
  assert.deepEqual(requests[0].uploads, requests[1].uploads);
  assert.equal(requests[0].uploads.length, 1);
  assert.equal(provider.calls(), 1);
  const uploads = await (
    await context.request.get(
      url + "/api/uploads?scope=chat:john-lost-file-replies",
    )
  ).json();
  assert.equal(
    uploads.uploads.length,
    1,
    "Recovery must not upload a second copy",
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat retains a failed uploading message across reload and resumes when its file is chosen again", async (t) => {
  let received;
  const provider = await chatProvider(t, (res, body) => {
    received = body.messages.filter((m) => m.role === "user").at(-1).content;
    streamChunk(res, {
      role: "assistant",
      content: "Received the recovered file.",
    });
    streamFinish(res);
  });
  const { page, context } = await pageFor();
  await setupBrowserChat(page, provider.base, "mary-interrupted-file");
  let failing = true;
  await page.route("**/api/uploads/*?*", (route) =>
    route.request().method() === "PUT" && failing
      ? route.abort()
      : route.continue(),
  );
  const file = {
    name: "letter.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Original file bytes"),
  };
  await page.getByLabel("Attach files", { exact: true }).setInputFiles(file);
  await page
    .locator(".chat-attachment .error")
    .getByText("Upload failed. Retry this file.", { exact: true })
    .waitFor();
  const input = page.getByRole("textbox", {
    name: "Message Songya",
    exact: true,
  });
  await input.fill("Keep my original message.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page
    .locator(".chat-error")
    .getByText(/Retry sends the same request/)
    .waitFor();
  assert.equal(provider.calls(), 0);
  await input.fill("My next draft.");
  await page.reload();
  const choose = page
    .locator(".chat-message")
    .getByLabel("Choose letter.txt again", { exact: true });
  await choose.waitFor();
  failing = false;
  await choose.setInputFiles(file);
  await page
    .locator(".chat-message.assistant")
    .getByText("Received the recovered file.", { exact: true })
    .waitFor();
  assert.match(received, /Keep my original message\./);
  assert.match(received, /letter\.txt/);
  assert.equal(await input.inputValue(), "My next draft.");
  assert.equal(provider.calls(), 1);
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat reasoning stays open while streaming, collapses for the reply, and can be reopened", async (t) => {
  let release, moreThoughts, finish;
  const gate = new Promise((resolve) => (release = resolve));
  const thoughts = new Promise((resolve) => (moreThoughts = resolve));
  const finished = new Promise((resolve) => (finish = resolve));
  t.after(() => {
    moreThoughts();
    release();
    finish();
  });
  const provider = await chatProvider(t, async (res) => {
    streamChunk(res, {
      role: "assistant",
      reasoning_content: "Model reasoning sentinel",
    });
    await thoughts;
    streamChunk(res, { reasoning: " More live reasoning." });
    await gate;
    streamChunk(res, { content: "Here is the answer." });
    await finished;
    streamFinish(res);
  });
  const { page, context } = await pageFor();
  await setupBrowserChat(page, provider.base, "john-thinking");
  const input = page.getByRole("textbox", {
    name: "Message Songya",
    exact: true,
  });
  await input.fill("Hello.");
  await input.press("Enter");
  await page
    .locator(".chat-progress")
    .getByText(/^Thinking…/)
    .waitFor();
  const reasoning = page
    .locator(".chat-reasoning")
    .filter({ hasText: "Model reasoning sentinel" });
  await reasoning
    .getByText("Model reasoning sentinel", { exact: true })
    .waitFor();
  assert.equal(await reasoning.getAttribute("open"), "");
  const before = await page.evaluate(
    () =>
      window.Alpine.$data(document.querySelector("[x-data]")).chatState
        .revision,
  );
  await input.fill("My next draft.");
  await page.waitForTimeout(500);
  const after = await page.evaluate(
    () =>
      window.Alpine.$data(document.querySelector("[x-data]")).chatState
        .revision,
  );
  assert.equal(
    after,
    before,
    "Thinking activity must not rewrite an empty transcript",
  );
  assert.equal(await input.isEnabled(), true);
  moreThoughts();
  await reasoning
    .locator("pre")
    .getByText("Model reasoning sentinel More live reasoning.", { exact: true })
    .waitFor();
  assert.equal(await reasoning.getAttribute("open"), "");
  release();
  await page
    .locator(".chat-message.assistant")
    .getByText("Here is the answer.", { exact: true })
    .waitFor();
  assert.equal(await input.inputValue(), "My next draft.");
  assert.equal(await reasoning.getAttribute("open"), null);
  await reasoning.locator("summary").click();
  await reasoning
    .getByText("Model reasoning sentinel More live reasoning.", { exact: true })
    .waitFor();
  assert.equal(await reasoning.getAttribute("open"), "");
  finish();
  await page
    .locator(".chat-progress")
    .getByText("Reply complete.", { exact: true })
    .waitFor();
  assert.equal(
    await reasoning.getAttribute("open"),
    "",
    "A manual expansion must survive later reply updates",
  );
  await page.reload();
  const savedReasoning = page
    .locator(".chat-reasoning")
    .filter({ hasText: "Model reasoning sentinel" });
  await savedReasoning.locator("summary").waitFor();
  assert.equal(await savedReasoning.getAttribute("open"), null);
  await savedReasoning.locator("summary").click();
  await savedReasoning
    .getByText("Model reasoning sentinel More live reasoning.", { exact: true })
    .waitFor();
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat recovers completed replies through HTTP when live events cannot reconnect", async (t) => {
  let release;
  const gate = new Promise((resolve) => (release = resolve));
  t.after(() => release());
  const provider = await chatProvider(t, async (res) => {
    await gate;
    streamChunk(res, { role: "assistant", content: "The saved answer." });
    streamFinish(res);
  });
  const { page, context } = await pageFor();
  await setupBrowserChat(page, provider.base, "mary-disconnected");
  await page.route("**/api/chat/sessions/*/events", (route) => route.abort());
  const input = page.getByRole("textbox", {
    name: "Message Songya",
    exact: true,
  });
  await input.fill("Please answer.");
  await input.press("Enter");
  await page
    .locator(".chat-progress")
    .getByText("Live updates interrupted. Checking saved progress…", {
      exact: true,
    })
    .waitFor();
  await input.fill("My unsent draft.");
  release();
  await page
    .locator(".chat-progress")
    .getByText("Reply complete.", { exact: true })
    .waitFor();
  await page
    .locator(".chat-message.assistant")
    .getByText("The saved answer.", { exact: true })
    .waitFor();
  assert.equal(
    provider.calls(),
    1,
    "Reconnecting must never resend the user message",
  );
  assert.equal(await input.inputValue(), "My unsent draft.");
  await context.close();
  assert.deepEqual(errors, []);
});

test("chat detects a silent live connection and reloads saved progress without resending", async (t) => {
  const provider = await chatProvider(t, (res) => {
    streamChunk(res, { role: "assistant", content: "Recovered from silence." });
    streamFinish(res);
  });
  const { page, context } = await pageFor();
  await page.addInitScript(() => {
    const Original = window.EventSource;
    window.EventSource = class {
      static OPEN = 1;
      constructor(url) {
        if (!url.includes("/api/chat/sessions/")) return new Original(url);
        this.readyState = 1;
        queueMicrotask(() => this.onopen?.(new Event("open")));
      }
      addEventListener() {}
      close() {
        this.readyState = 2;
      }
    };
  });
  await page.reload();
  await setupBrowserChat(page, provider.base, "john-silent-stream");
  const input = page.getByRole("textbox", {
    name: "Message Songya",
    exact: true,
  });
  await input.fill("Please answer once.");
  await input.press("Enter");
  await input.fill("Still typing.");
  await page
    .locator(".chat-progress")
    .getByText("Reply complete.", { exact: true })
    .waitFor({ timeout: 22000 });
  await page
    .locator(".chat-message.assistant")
    .getByText("Recovered from silence.", { exact: true })
    .waitFor();
  assert.equal(provider.calls(), 1);
  assert.equal(await input.inputValue(), "Still typing.");
  await context.close();
  assert.deepEqual(errors, []);
});

test("entity profile pictures preserve originals, crop consistently, and survive reload and missing files", async () => {
  const record = await save(
    source("Mary portrait", "Original identifying words."),
    "entities",
  );
  const { page, context } = await pageFor();
  const bytes = Buffer.from(
    await page.evaluate(() => {
      const c = document.createElement("canvas");
      c.width = 200;
      c.height = 100;
      const ctx = c.getContext("2d");
      ctx.fillStyle = "red";
      ctx.fillRect(0, 0, 100, 100);
      ctx.fillStyle = "blue";
      ctx.fillRect(100, 0, 100, 100);
      return c.toDataURL("image/png").split(",")[1];
    }),
    "base64",
  );
  await editRecord(page, "Mary portrait");
  await page.locator(".record-files input[type=file][multiple]").setInputFiles({
    name: "portrait.png",
    mimeType: "image/png",
    buffer: bytes,
  });
  await page
    .getByRole("button", { name: "Open photo portrait.png", exact: true })
    .click();
  const choose = page.getByRole("button", {
    name: "Use as profile picture",
    exact: true,
  });
  await choose.waitFor();
  await choose.click();
  await page.getByLabel("Horizontal focus", { exact: true }).fill("1");
  await page.getByLabel("Profile picture zoom", { exact: true }).fill("2");
  await page.waitForFunction(() => {
    const c = document.querySelector(".entity-portrait");
    if (!c || c.hidden) return false;
    const p = c.getContext("2d").getImageData(128, 128, 1, 1).data;
    return p[2] > 240 && p[0] < 10;
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  let graph = await (await fetch(url + "/api/graph")).json();
  assert.deepEqual(
    graph.nodes.find((n) => n.id === record.id).profile_picture,
    { filename: "portrait.png", x: 1, y: 0.5, zoom: 2 },
  );
  assert.deepEqual(
    Buffer.from(
      await (
        await fetch(url + "/api/records/" + record.id + "/files/portrait.png")
      ).arrayBuffer(),
    ),
    bytes,
  );
  await page.evaluate(() => {
    window.portraitRenders = [];
    const original = CanvasRenderingContext2D.prototype.drawImage;
    CanvasRenderingContext2D.prototype.drawImage = function (image, ...args) {
      if (
        image instanceof HTMLImageElement &&
        image.src.includes("portrait.png") &&
        !this.canvas.classList.contains("entity-portrait")
      )
        window.portraitRenders.push({
          width: this.canvas.width,
          crop: args.slice(0, 4),
        });
      return original.call(this, image, ...args);
    };
  });
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.locator(".graph-legend").scrollIntoViewIfNeeded();
  await page.waitForFunction(() => window.portraitRenders.length > 0);
  assert.ok(
    (await page.evaluate(() => window.portraitRenders)).some(
      (x) => x.crop[0] === 150 && x.crop[2] === 50,
    ),
  );
  await page.evaluate(() => (window.portraitRenders = []));
  await page.getByRole("button", { name: "3D", exact: true }).click();
  await page.waitForFunction(() =>
    window.portraitRenders.some((x) => x.width === 256),
  );
  await page.getByRole("button", { name: "2D", exact: true }).click();
  await page.reload();
  await editRecord(page, "Mary portrait");
  assert.equal(
    await page.getByLabel("Profile picture zoom", { exact: true }).inputValue(),
    "2",
  );
  await page
    .getByRole("button", { name: "Open photo portrait.png", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Edit profile picture", exact: true })
    .click();
  assert.equal(
    await page.getByLabel("Profile picture zoom", { exact: true }).inputValue(),
    "2",
    "Editing does not reset crop",
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ colorScheme: "dark" });
  await page
    .getByLabel("Profile picture zoom", { exact: true })
    .scrollIntoViewIfNeeded();
  assert.equal(
    await page
      .locator(".portrait-editor")
      .evaluate((el) => el.scrollWidth <= el.clientWidth),
    true,
  );
  await page.screenshot({ path: "/tmp/songya-profile-editor.png" });
  await page
    .getByRole("button", { name: "Remove profile picture", exact: true })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  graph = await (await fetch(url + "/api/graph")).json();
  assert.equal(
    graph.nodes.find((n) => n.id === record.id).profile_picture,
    null,
  );
  assert.equal(
    (await (await fetch(url + "/api/records/" + record.id)).json()).draft.body,
    "Original identifying words.",
  );
  assert.equal(
    (await (await fetch(url + "/api/records/" + record.id + "/files")).json())
      .files.length,
    1,
  );
  const current = await (await fetch(url + "/api/records/" + record.id)).json();
  await save(
    current.source.replace(
      "---\n",
      "---\nprofile_picture: {filename: missing.png, x: 0.5, y: 0.5, zoom: 1}\n",
    ),
    "entities",
    record.id,
    current.version,
  );
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.reload();
  await editRecord(page, "Mary portrait");
  await page
    .getByRole("alert")
    .filter({ hasText: "This image could not be displayed" })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "Remove profile picture", exact: true })
      .isEnabled(),
    true,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("graph legend rebuilds connections through hidden kinds in both views", async () => {
  const { page, context } = await pageFor(undefined, () => {
    window.projectedLineDraws = 0;
    const setLineDash = CanvasRenderingContext2D.prototype.setLineDash;
    CanvasRenderingContext2D.prototype.setLineDash = function (segments) {
      if (segments[0] === 3 && segments[1] === 3) window.projectedLineDraws++;
      return setLineDash.call(this, segments);
    };
  });
  const fixture = {
    nodes: [
      { id: "e_john", title: "Projection John", kind: "entities" },
      { id: "e_mary", title: "Projection Mary", kind: "entities" },
      { id: "m_meal", title: "Projection meal", kind: "memories", user: "ari" },
      { id: "t_music", title: "Projection music", kind: "topics" },
      {
        id: "s_visit",
        title: "Projection visit",
        kind: "stories",
        user: "ari",
      },
    ],
    links: [
      { source: "e_john", target: "m_meal", type: "related_to" },
      { source: "m_meal", target: "t_music", type: "related_to" },
      { source: "t_music", target: "s_visit", type: "related_to" },
      { source: "s_visit", target: "e_mary", type: "related_to" },
      { source: "e_mary", target: "e_john", type: "related_to" },
    ],
  };
  await page.route("**/api/graph**", (route) =>
    route.fulfill({ json: fixture }),
  );
  await page.reload();
  await page.locator(".graph-legend").scrollIntoViewIfNeeded();
  const search = page.getByRole("searchbox", {
    name: "Search graph",
    exact: true,
  });
  await search.fill("Projection");
  await page.waitForFunction(
    () => window.Alpine.$data(document.body).graphMatches.length === 5,
  );
  assert.equal(await page.evaluate(() => window.projectedLineDraws), 0);
  for (const mode of ["2D", "3D"]) {
    await page.getByRole("button", { name: mode, exact: true }).click();
    for (const kind of ["entities", "memories", "topics", "stories"]) {
      const before = await page.evaluate(() => window.projectedLineDraws);
      const box = page.getByLabel("Show " + kind + " in graph", {
        exact: true,
      });
      await box.uncheck();
      await page.waitForFunction((kind) => {
        const matches = window.Alpine.$data(document.body).graphMatches;
        return (
          matches.length === (kind === "entities" ? 3 : 4) &&
          matches.every((node) => node.kind !== kind)
        );
      }, kind);
      if (mode === "2D")
        await page.waitForFunction(
          (before) => window.projectedLineDraws > before,
          before,
        );
      await box.check();
      await page.waitForFunction(
        () => window.Alpine.$data(document.body).graphMatches.length === 5,
      );
    }
  }
  await page.getByRole("button", { name: "2D", exact: true }).click();
  // A chain through three different hidden kinds still leaves John and Mary.
  for (const kind of ["memories", "topics", "stories"])
    await page
      .getByLabel("Show " + kind + " in graph", { exact: true })
      .uncheck();
  await page.waitForFunction(
    () => window.Alpine.$data(document.body).graphMatches.length === 2,
  );
  for (const kind of ["memories", "topics", "stories"])
    await page
      .getByLabel("Show " + kind + " in graph", { exact: true })
      .check();
  for (const kind of ["entities", "memories", "topics", "stories"]) {
    const box = page.getByLabel("Show " + kind + " in graph", { exact: true });
    assert.equal(await box.isChecked(), true);
    await box.uncheck();
  }
  await page.getByText("No record kinds selected.", { exact: true }).waitFor();
  await page.getByLabel("Show entities in graph", { exact: true }).check();
  await page.getByRole("button", { name: "3D", exact: true }).click();
  assert.equal(
    await page
      .getByLabel("Show memories in graph", { exact: true })
      .isChecked(),
    false,
  );
  for (const kind of ["memories", "topics", "stories"])
    await page
      .getByLabel("Show " + kind + " in graph", { exact: true })
      .check();
  await context.close();
  assert.deepEqual(errors, []);
});

test("graph refreshes preserve settled positions and camera for reordered records and weight changes", async () => {
  const { page, context } = await pageFor();
  const fixture = {
    nodes: [
      { id: "e_john", title: "John", kind: "entities", weight: 0 },
      { id: "m_meal", title: "Meal", kind: "memories", user: "ari", weight: 0 },
      { id: "t_food", title: "Food", kind: "topics", weight: 0 },
    ],
    links: [
      { source: "e_john", target: "m_meal", type: "related_to" },
      { source: "m_meal", target: "t_food", type: "related_to" },
    ],
  };
  await page.route("**/api/graph**", (route) =>
    route.fulfill({ json: fixture }),
  );
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  await page.waitForTimeout(3000);
  const before = await graphPoint(page, "green");
  assert.ok(before);
  fixture.nodes.reverse();
  fixture.links.reverse();
  fixture.nodes.find((node) => node.id === "e_john").weight = 2;
  await page.evaluate(() => window.Alpine.$data(document.body).refreshGraph());
  await page.waitForTimeout(1000);
  const after = await graphPoint(page, "green");
  assert.ok(after);
  assert.ok(
    Math.hypot(after.x - before.x, after.y - before.y) < 2,
    "A metadata refresh moved the settled graph",
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("entity portraits can reference linked memory photos without copying files", async () => {
  const entity = await save(
    source("John linked portrait", "Keep identity words."),
    "entities",
  );
  const memory = await save(
    source(
      "John photo memory",
      "Keep memory words.",
      `relations:\n  - type: related_to\n    target: ${entity.id}\n`,
    ),
  );
  const other = await save(source("Other photo memory", "Not linked to John."));
  const { page, context } = await pageFor();
  const bytes = Buffer.from(
    await page.evaluate(() => {
      const c = document.createElement("canvas");
      c.width = c.height = 16;
      const ctx = c.getContext("2d");
      ctx.fillStyle = "green";
      ctx.fillRect(0, 0, 16, 16);
      return c.toDataURL().split(",")[1];
    }),
    "base64",
  );
  for (const record of [memory, other]) {
    const upload = await (
      await fetch(
        url +
          "/api/uploads/" +
          crypto.randomUUID() +
          "?scope=test&filename=same.png",
        { method: "PUT", body: bytes },
      )
    ).json();
    const receipt = await (
      await fetch(url + "/api/records/" + record.id + "/files", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          key: crypto.randomUUID(),
          revision: record.version,
          action: "add",
          upload: upload.upload,
        }),
      })
    ).json();
    assert.equal(receipt.status, "committed");
  }
  await editRecord(page, "John linked portrait");
  const gallery = page.getByRole("region", {
    name: "Photos from memories",
  });
  assert.equal(await gallery.locator(".photo-tile").count(), 1);
  await gallery.getByRole("button", { name: "Open photo same.png" }).click();
  const photo = page.getByRole("dialog", {
    name: "Photo details",
    exact: true,
  });
  await photo.waitFor();
  assert.equal(
    await photo
      .getByRole("link", { name: "Download original" })
      .getAttribute("href"),
    `/api/records/${memory.id}/files/same.png`,
  );
  assert.equal(
    await photo.getByRole("button", { name: "Remove permanently" }).count(),
    0,
    "Linked files are managed on their source memory",
  );
  await page.keyboard.press("Escape");
  assert.equal(
    await page.locator('dialog[x-ref="editorDialog"]').isVisible(),
    true,
  );
  await gallery.getByRole("button", { name: "Open photo same.png" }).click();
  await photo
    .getByRole("button", { name: "Use as profile picture", exact: true })
    .click();
  await page.getByLabel("Profile picture zoom", { exact: true }).fill("1.75");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const graph = await (await fetch(url + "/api/graph")).json();
  assert.deepEqual(
    graph.nodes.find((n) => n.id === entity.id).profile_picture,
    { record: memory.id, filename: "same.png", x: 0.5, y: 0.5, zoom: 1.75 },
  );
  const entityFiles = await (
    await fetch(url + "/api/records/" + entity.id + "/files")
  ).json();
  assert.equal(
    entityFiles.files.length,
    0,
    "No attachment copied into the entity",
  );
  assert.deepEqual(
    Buffer.from(
      await (
        await fetch(url + "/api/records/" + memory.id + "/files/same.png")
      ).arrayBuffer(),
    ),
    bytes,
  );
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.reload();
  await editRecord(page, "John linked portrait");
  await page.waitForFunction(() => {
    const c = document.querySelector(".portrait-preview");
    return c && !c.hidden;
  });
  assert.equal(
    await page.getByLabel("Profile picture zoom", { exact: true }).inputValue(),
    "1.75",
  );
  await page
    .getByRole("button", { name: "Open photo same.png", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Edit profile picture", exact: true })
    .click();
  assert.equal(
    await page.getByLabel("Profile picture zoom", { exact: true }).inputValue(),
    "1.75",
  );
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.evaluate(
    (id) => window.Alpine.$data(document.body).open(id, false, true),
    entity.id,
  );
  const readGallery = page.getByRole("region", {
    name: "Photos from memories",
  });
  await readGallery
    .getByRole("button", { name: "Open photo same.png" })
    .click();
  const viewer = page.getByRole("dialog", {
    name: "Photo details",
    exact: true,
  });
  assert.equal(
    await viewer
      .getByRole("button", { name: "Use as profile picture" })
      .isVisible(),
    false,
  );
  await viewer
    .getByRole("link", { name: "John photo memory", exact: true })
    .click();
  await page.waitForFunction(
    (id) => window.Alpine.$data(document.body).selected?.id === id,
    memory.id,
  );
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await editRecord(page, "John linked portrait");
  const beforeRevision = graph.nodes.find((n) => n.id === entity.id).revision;
  const originalMemory = await (
    await fetch(url + "/api/records/" + memory.id)
  ).json();
  await save(
    originalMemory.source + "\nOne more detail.",
    "memories",
    memory.id,
    originalMemory.version,
  );
  const changedGraph = await (await fetch(url + "/api/graph")).json();
  assert.notEqual(
    changedGraph.nodes.find((n) => n.id === entity.id).revision,
    beforeRevision,
    "Source changes invalidate the graph image cache",
  );
  const listing = await (
    await fetch(url + "/api/records/" + memory.id + "/files")
  ).json();
  await fetch(url + "/api/records/" + memory.id + "/files", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      key: crypto.randomUUID(),
      revision: listing.revision,
      action: "remove",
      filename: "same.png",
    }),
  });
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.reload();
  await editRecord(page, "John linked portrait");
  await page
    .getByRole("alert")
    .filter({ hasText: "This image could not be displayed" })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "Remove profile picture", exact: true })
      .isEnabled(),
    true,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("photo masonry opens accessible details and keeps file actions in the viewer", async () => {
  const record = await save(
    source("Mary gallery", "Preserve these words."),
    "entities",
  );
  const { page, context } = await pageFor();
  const bytes = Buffer.from(
    await page.evaluate(() => {
      const c = document.createElement("canvas");
      c.width = 240;
      c.height = 160;
      c.getContext("2d").fillRect(0, 0, 240, 160);
      return c.toDataURL("image/png").split(",")[1];
    }),
    "base64",
  );
  await editRecord(page, "Mary gallery");
  await page.locator(".record-files input[type=file][multiple]").setInputFiles([
    { name: "first.png", mimeType: "image/png", buffer: bytes },
    { name: "second.png", mimeType: "image/png", buffer: bytes },
  ]);
  const gallery = page.getByLabel("Attached photos", { exact: true });
  await gallery
    .getByRole("button", { name: "Open photo second.png" })
    .waitFor();
  assert.equal(await gallery.locator("figcaption").count(), 0);
  assert.equal(
    await gallery.getByRole("button", { name: "Remove permanently" }).count(),
    0,
  );
  await gallery.getByRole("button", { name: "Open photo first.png" }).click();
  const viewer = page.getByRole("dialog", {
    name: "Photo details",
    exact: true,
  });
  await viewer.waitFor();
  await page.waitForFunction(
    () => document.querySelector(".photo-full")?.naturalWidth === 240,
  );
  assert.equal(
    await viewer.getByRole("heading", { name: "first.png" }).isVisible(),
    true,
  );
  assert.equal(
    await viewer
      .getByRole("link", { name: "Download original" })
      .getAttribute("href"),
    `/api/records/${record.id}/files/first.png`,
  );
  for (const colorScheme of ["dark", "light"]) {
    await page.emulateMedia({ colorScheme });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(
      await viewer.evaluate((el) => el.scrollWidth <= el.clientWidth),
      true,
    );
  }
  const replacement = Buffer.from(
    await page.evaluate(() => {
      const c = document.createElement("canvas");
      c.width = 120;
      c.height = 80;
      c.getContext("2d").fillRect(0, 0, 120, 80);
      return c.toDataURL("image/png").split(",")[1];
    }),
    "base64",
  );
  page.once("dialog", (dialog) => dialog.accept());
  await viewer.locator("input[type=file]").setInputFiles({
    name: "replacement.png",
    mimeType: "image/png",
    buffer: replacement,
  });
  await page.waitForFunction(
    () => document.querySelector(".photo-full")?.naturalWidth === 120,
  );
  assert.deepEqual(
    Buffer.from(
      await (
        await fetch(url + `/api/records/${record.id}/files/first.png`)
      ).arrayBuffer(),
    ),
    replacement,
  );
  await page.screenshot({ path: "/tmp/songya-photo-details.png" });
  page.once("dialog", (dialog) => dialog.dismiss());
  await viewer.getByRole("button", { name: "Remove permanently" }).click();
  assert.equal(
    await viewer.isVisible(),
    true,
    "Cancel leaves the photo and viewer intact",
  );
  page.once("dialog", (dialog) => dialog.accept());
  await viewer.getByRole("button", { name: "Remove permanently" }).click();
  await viewer.waitFor({ state: "hidden" });
  assert.equal(await gallery.getByRole("button").count(), 1);
  assert.equal(
    await page.locator('dialog[x-ref="editorDialog"]').isVisible(),
    true,
  );
  assert.equal(
    (await (await fetch(url + "/api/records/" + record.id)).json()).draft.body,
    "Preserve these words.",
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("weighted graph sizes count entry text, ignore dates and update after body and connection edits", async () => {
  const john = await save(
    source("John weighted graph", "An identity."),
    "entities",
  );
  const mary = await save(
    source("Mary weighted graph", "Another identity."),
    "entities",
  );
  const event = (title, id, date) =>
    source(
      title,
      "An experience.",
      `time:\n  date: ${date}\nrelations:\n  - type: related_to\n    target: ${id}\n`,
    );
  const first = await save(event("John's event", john.id, "1900-01-01"));
  await save(
    source(
      "John's enduring memory",
      "An experience.",
      `relations:\n  - type: related_to\n    target: ${john.id}\n`,
    ),
  );
  await save(event("Mary's event", mary.id, "2200-01-01"));
  const { page, context } = await pageFor();
  await page.locator(".graph-legend").scrollIntoViewIfNeeded();
  await page.waitForFunction(
    async (ids) => {
      const data = await (await fetch("/api/graph")).json();
      const [a, b] = ids.map((id) => data.nodes.find((n) => n.id === id));
      return a?.weight === 4 && b?.weight === 3;
    },
    [john.id, mary.id],
  );
  await page.evaluate(() => {
    window.weightRadii = [];
    const arc = CanvasRenderingContext2D.prototype.arc;
    CanvasRenderingContext2D.prototype.arc = function (x, y, r, ...args) {
      if (this.canvas.closest(".graph-canvas")) {
        window.weightRadii.push(r);
        if (window.weightRadii.length > 10000)
          window.weightRadii.splice(0, 5000);
      }
      return arc.call(this, x, y, r, ...args);
    };
    return window.Alpine.$data(document.body).refreshGraph();
  });
  const graph = await (await fetch(url + "/api/graph")).json();
  const weights = [john, mary].map(
    (r) => graph.nodes.find((n) => n.id === r.id).weight,
  );
  await page.waitForFunction(
    (values) =>
      values.every((v) =>
        window.weightRadii.some(
          (r) => Math.abs(r - 4 * Math.sqrt(1 + v)) < 1e-6,
        ),
      ),
    weights,
  );
  const saved = await (await fetch(url + "/api/records/" + first.id)).json();
  await save(
    event("John's event", john.id, "2000-01-01"),
    "memories",
    first.id,
    saved.version,
  );
  const calculated = graph.nodes.find(
    (node) => node.id === john.id,
  ).weight_calculated_at;
  await page.waitForFunction(
    async ({ id, calculated }) => {
      const graph = await (await fetch("/api/graph")).json();
      const node = graph.nodes.find((n) => n.id === id);
      return node?.weight === 4 && node.weight_calculated_at > calculated;
    },
    { id: john.id, calculated },
  );
  const identity = await (await fetch(url + "/api/records/" + john.id)).json();
  await save(
    source("John weighted graph", "An identity with more recorded words."),
    "entities",
    john.id,
    identity.version,
  );
  await page.waitForFunction(async (id) => {
    const graph = await (await fetch("/api/graph")).json();
    return graph.nodes.find((n) => n.id === id)?.weight === 8;
  }, john.id);
  const edited = await (await fetch(url + "/api/records/" + first.id)).json();
  await save(
    source("John's event", "An experience.", "time: {date: 2000-01-01}\n"),
    "memories",
    first.id,
    edited.version,
  );
  await page.waitForFunction(async (id) => {
    const graph = await (await fetch("/api/graph")).json();
    return graph.nodes.find((n) => n.id === id)?.weight === 0.5;
  }, john.id);
  await page.waitForFunction(() =>
    window.weightRadii.some((r) => Math.abs(r - 4 * Math.sqrt(8)) < 1e-6),
  );
  await page.getByRole("button", { name: "3D", exact: true }).click();
  await page.getByRole("button", { name: "2D", exact: true }).click();
  assert.equal(await page.locator(".graph-weight-status").isVisible(), true);
  await context.close();
  assert.deepEqual(errors, []);
});
