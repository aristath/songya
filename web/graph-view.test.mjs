import { test } from "node:test";
import assert from "node:assert/strict";
import { projectGraph } from "./src/graph-view.js";

const node = (id, kind = "entities") => ({ id, kind });
const link = (source, target, type = "related_to") => ({
  source,
  target,
  type,
});
const pairs = (data) =>
  data.links.map((e) => [e.source, e.target].sort().join(":")).sort();

test("a hidden memory connects its participants without inventing a directed fact", () => {
  const data = {
    nodes: [node("John"), node("Mary"), node("meal", "memories")],
    links: [link("meal", "John"), link("meal", "Mary")],
  };
  const before = structuredClone(data);
  const view = projectGraph(data, { memories: false });
  assert.deepEqual(
    view.nodes.map((n) => n.id),
    ["John", "Mary"],
  );
  assert.deepEqual(pairs(view), ["John:Mary"]);
  assert.equal(view.links[0].projected, true);
  assert.equal(view.links[0].type, "Connected through hidden records");
  assert.ok(view.links[0].strength > 0 && view.links[0].strength < 1);
  assert.deepEqual(data, before, "The source graph must not be mutated");
  assert.deepEqual(projectGraph(data, {}), data);
  assert.deepEqual(projectGraph(data, { entities: false, memories: false }), {
    nodes: [],
    links: [],
  });
});

test("every hidden kind and mixed hidden chains preserve connectivity", () => {
  const kinds = ["entities", "memories", "topics", "stories"];
  const data = {
    nodes: kinds.map((kind, i) => node(String(i), kind)),
    links: [link("0", "1"), link("1", "2"), link("2", "3")],
  };
  for (let mask = 0; mask < 16; mask++) {
    const filters = Object.fromEntries(
      kinds.map((k, i) => [k, !!(mask & (1 << i))]),
    );
    const view = projectGraph(data, filters);
    const ids = view.nodes.map((n) => n.id);
    assert.equal(view.links.length, Math.max(0, ids.length - 1));
    assert.deepEqual(
      pairs(view),
      ids.slice(1).map((id, i) => `${ids[i]}:${id}`),
    );
    assert.ok(
      view.links.every((e) => ids.includes(e.source) && ids.includes(e.target)),
    );
  }
});

test("visible intermediaries remain separate and direct relationships keep their types", () => {
  const direct = link("John", "Mary", "after");
  const view = projectGraph(
    {
      nodes: [
        node("John"),
        node("Mary"),
        node("Tom"),
        node("meal", "memories"),
      ],
      links: [
        direct,
        link("meal", "John"),
        link("meal", "Mary"),
        link("Mary", "Tom"),
      ],
    },
    { memories: false },
  );
  assert.deepEqual(pairs(view), ["John:Mary", "Mary:Tom"]);
  assert.deepEqual(view.links[0], direct);
  assert.equal(
    view.links.some((e) => e.projected),
    false,
  );
});

test("independent shared memories add support while a broad hidden hub dilutes it", () => {
  const data = {
    nodes: [
      node("John"),
      node("Mary"),
      node("a", "memories"),
      node("b", "memories"),
    ],
    links: [
      link("a", "John"),
      link("a", "Mary"),
      link("b", "John"),
      link("b", "Mary"),
    ],
  };
  const strong = projectGraph(data, { memories: false }).links[0].strength;
  const hub = {
    nodes: [
      ...data.nodes.filter((n) => n.id !== "b"),
      ...Array.from({ length: 8 }, (_, i) => node(`extra${i}`)),
    ],
    links: [
      ...data.links.filter((e) => e.source !== "b"),
      ...Array.from({ length: 8 }, (_, i) => link("a", `extra${i}`)),
    ],
  };
  const weak = projectGraph(hub, { memories: false }).links.find(
    (e) => e.source === "John" && e.target === "Mary",
  ).strength;
  assert.ok(strong > weak);
});

test("hidden cycles terminate and excluded-owner or missing nodes cannot act as bridges", () => {
  const data = {
    nodes: [
      node("John"),
      node("Mary"),
      node("Tom"),
      node("a", "memories"),
      node("b", "memories"),
      node("c", "stories"),
    ],
    links: [
      link("John", "a"),
      link("a", "b"),
      link("b", "c"),
      link("c", "a"),
      link("c", "Mary"),
      link("Mary", "other-owner"),
      link("other-owner", "Tom"),
    ],
  };
  const view = projectGraph(data, { memories: false, stories: false });
  assert.deepEqual(pairs(view), ["John:Mary"]);
  assert.equal(
    view.nodes.some((n) => n.id === "Tom"),
    true,
  );
});

test("hiding a broad shared record keeps a connected view without an all-pairs hairball", () => {
  const nodes = Array.from({ length: 40 }, (_, i) => node(`person${i}`));
  const view = projectGraph(
    {
      nodes: [...nodes, node("shared", "topics")],
      links: nodes.map((n) => link(n.id, "shared")),
    },
    { topics: false },
  );
  assert.equal(view.nodes.length, nodes.length);
  assert.ok(view.links.length < nodes.length * 5);
  const reachable = new Set([nodes[0].id]);
  for (let size = -1; size !== reachable.size;) {
    size = reachable.size;
    for (const { source, target } of view.links) {
      if (reachable.has(source)) reachable.add(target);
      if (reachable.has(target)) reachable.add(source);
    }
  }
  assert.equal(reachable.size, nodes.length);
});
