import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  readdirSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";
import { chromium } from "playwright";
let root, processHandle, url, browser, embeddingMock;
const errors = [];
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
    browser = await chromium.launch({ headless: true });
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
) {
  const context = await browser.newContext({ viewport });
  if (init) await context.addInitScript(init);
  const requests = [];
  context.on("request", (request) => requests.push(request.url()));
  await context.route("**/*", (route) =>
    route.request().url().startsWith(url) ? route.continue() : route.abort(),
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
  await page.goto(url);
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
  const png = (await canvas.screenshot()).toString("base64");
  return page.evaluate(
    async ({ png, color }) => {
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
      for (let y = 15; y < image.height - 45; y++) {
        for (let x = 15; x < image.width - 15; x++) {
          const i = (y * image.width + x) * 4;
          const [r, g, b] = pixels.slice(i, i + 3);
          const score =
            color === "green"
              ? g - Math.max(r, b)
              : color === "red"
                ? r - Math.max(g, b)
                : color === "amber"
                  ? Math.min(r, g) - b
                  : Math.min(r, g, b) - Math.max(r, g, b) * 0.5;
          if (score > 50) {
            sumX += x;
            sumY += y;
            count++;
          }
        }
      }
      return count ? { x: sumX / count, y: sumY / count } : null;
    },
    { png, color },
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
    n.endsWith(".md"),
  );
  assert.equal(files.length, 1);
  assert.match(
    readFileSync(join(root, "store", "memories", files[0]), "utf8"),
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
  const damagedPath = join(root, "store", "memories", "m_000000000098.md");
  t.after(() => rmSync(damagedPath, { force: true }));
  const id = "m_000000000099";
  writeFileSync(
    join(root, "store", "memories", id + ".md"),
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
  await page.getByLabel("Filter title", { exact: true }).fill("Table ");
  assert.equal(await page.locator(".record").count(), 3);
  assert.equal(await page.locator("aside").count(), 0);
  assert.equal(await page.locator("dialog:modal").count(), 0);
  const titles = () =>
    page.locator(".title-cell .text-button").allTextContents();
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
  await page.route("**/api/graph", (route) => route.fulfill({ json: data }));
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
    "rgb(0, 0, 0)",
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
  await page.waitForTimeout(600);
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
  await page.route("**/api/graph", (route) => route.fulfill({ json: data }));
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
  await page.route("**/api/graph", (route) =>
    route.fulfill({ json: { nodes, links } }),
  );
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  await page.waitForTimeout(3500);
  const png = (
    await page.locator(".graph-canvas canvas").screenshot()
  ).toString("base64");
  const disks = await page.evaluate(async (png) => {
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
    // The DOM legend overlays the canvas; exclude its bottom strip.
    for (let i = 0; i < image.width * (image.height - 32); i++)
      green[i] = data[i * 4 + 1] - Math.max(data[i * 4], data[i * 4 + 2]) > 50;
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
  }, png);
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
      assert.equal(body.tools.length, 15);
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
  await page
    .getByLabel("Message Songya", { exact: true })
    .fill("Remember that I moved to the island on 12 June 2001.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  const log = page.getByRole("log", { name: "Chat messages", exact: true });
  await log.getByText("Saved your move.", { exact: true }).waitFor();
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
  assert.equal(await background(), "rgb(23, 30, 37)");
  await theme.selectOption("light");
  assert.equal(await background(), "rgb(255, 255, 255)");
  await page.reload();
  assert.equal(await theme.inputValue(), "light");
  assert.equal(await background(), "rgb(255, 255, 255)");
  await theme.selectOption("system");
  assert.equal(await background(), "rgb(23, 30, 37)");
  await page.emulateMedia({ colorScheme: "light" });
  assert.equal(await background(), "rgb(255, 255, 255)");
  await theme.selectOption("dark");
  assert.equal(await background(), "rgb(23, 30, 37)");
  await page.locator("#list-panel").scrollIntoViewIfNeeded();
  assert.equal(
    await page.locator("main").evaluate((el) => getComputedStyle(el).color),
    "rgb(224, 232, 238)",
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
    await added.locator("input").fill("Ingredient");
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
