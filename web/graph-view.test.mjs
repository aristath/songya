import { test } from "node:test";
import assert from "node:assert/strict";
import { projectGraph, graphLinks } from "./src/graph-view.js";

const node = (id, kind = "entities") => ({ id, kind });
const link = (source, target, type = "related_to") => ({
  source,
  target,
  type,
});
const connections = (data) =>
  graphLinks(data)
    .map((edge) => JSON.stringify(edge))
    .sort();

test("hidden records remain named junctions with their actual relationships", () => {
  const data = {
    nodes: [node("John"), node("Mary"), node("meal", "memories")],
    links: [link("meal", "John"), link("meal", "Mary")],
  };
  const before = structuredClone(data);
  const view = projectGraph(data, { memories: false });
  assert.deepEqual(
    view.nodes.filter((n) => !n.junction).map((n) => n.id),
    ["John", "Mary"],
  );
  assert.equal(view.nodes.find((n) => n.id === "meal").junction, true);
  assert.deepEqual(connections(view), connections(data));
  assert.equal(
    view.links.some((e) => e.strength || e.projected),
    false,
  );
  assert.deepEqual(data, before);
  assert.ok(projectGraph(data, {}).nodes.every((n) => !n.junction));
  assert.deepEqual(projectGraph(data, { entities: false, memories: false }), {
    nodes: [],
    links: [],
  });
});

test("filtering every combination preserves paths without adding relationships", () => {
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
    const selected = data.nodes.filter((n) => filters[n.kind]);
    assert.deepEqual(
      view.nodes.filter((n) => !n.junction).map((n) => n.id),
      selected.map((n) => n.id),
    );
    if (!selected.length) {
      assert.equal(view.links.length, 0);
      continue;
    }
    const first = Number(selected[0].id),
      last = Number(selected.at(-1).id);
    assert.deepEqual(
      view.nodes.map((n) => n.id),
      data.nodes.slice(first, last + 1).map((n) => n.id),
    );
    assert.deepEqual(
      connections(view),
      connections({ nodes: view.nodes, links: data.links }),
    );
  }
});

test("a hidden inventory cannot manufacture plant connections or member hubs", () => {
  const plants = Array.from({ length: 40 }, (_, i) => node(`plant${i}`));
  const data = {
    nodes: [node("garden"), ...plants, node("inventory", "memories")],
    links: [
      ...plants.map((p) => link(p.id, "garden")),
      ...plants.map((p) => link("inventory", p.id)),
      link("inventory", "garden"),
    ],
  };
  const view = projectGraph(data, { memories: false });
  assert.deepEqual(connections(view), connections(data));
  for (const plant of plants)
    assert.equal(
      view.links.filter((e) => e.source === plant.id || e.target === plant.id)
        .length,
      2,
    );
  const reordered = projectGraph(
    { nodes: [...data.nodes].reverse(), links: [...data.links].reverse() },
    { memories: false },
  );
  assert.deepEqual(connections(reordered), connections(view));
});

test("chronology, causality and uncertain identity retain their meaning through hidden records", () => {
  for (const type of ["after", "caused_by", "part_of", "possibly_same_as"]) {
    const data = {
      nodes: [
        node("John"),
        node("Mary"),
        node("a", "memories"),
        node("b", "memories"),
      ],
      links: [link("a", "John"), link("b", "Mary"), link("b", "a", type)],
    };
    const view = projectGraph(data, { memories: false });
    assert.deepEqual(connections(view), connections(data));
    assert.equal(
      view.links.some(
        (e) =>
          [e.source, e.target].includes("John") &&
          [e.source, e.target].includes("Mary"),
      ),
      false,
    );
  }
});

test("hidden dead ends, disconnected cycles and missing owner records do not clutter or bridge the view", () => {
  const data = {
    nodes: [
      node("John"),
      node("Mary"),
      node("leaf", "memories"),
      node("a", "stories"),
      node("b", "stories"),
      node("c", "stories"),
    ],
    links: [
      link("John", "leaf"),
      link("a", "b"),
      link("b", "c"),
      link("c", "a"),
      link("John", "other-owner"),
      link("other-owner", "Mary"),
    ],
  };
  const view = projectGraph(data, { memories: false, stories: false });
  assert.deepEqual(
    view.nodes.map((n) => n.id),
    ["John", "Mary"],
  );
  assert.deepEqual(view.links, []);
});

test("reciprocal symmetric links display once while directional relationships stay distinct", () => {
  const data = {
    nodes: [node("John"), node("Mary")],
    links: [
      link("John", "Mary"),
      link("Mary", "John"),
      link("John", "Mary", "possibly_same_as"),
      link("Mary", "John", "possibly_same_as"),
      link("John", "Mary", "after"),
      link("Mary", "John", "after"),
    ],
  };
  assert.equal(graphLinks(data).length, 4);
  assert.equal(graphLinks(data).filter((e) => e.type === "after").length, 2);
});
