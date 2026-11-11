import { test } from "node:test";
import assert from "node:assert/strict";
import {
  forceSimulation,
  forceLink,
  forceManyBody,
  forceCenter,
} from "d3-force-3d";
import { projectGraph, graphLinks, savedLinkForce } from "./src/graph-view.js";

const node = (id, kind = "entities") => ({ id, title: id, kind });
const link = (source, target, type = "related_to") => ({
  source,
  target,
  type,
});
const pairs = (data) =>
  data.links.map((edge) => [edge.source, edge.target].sort().join(":")).sort();

test("ownership keeps memories near their identity without creating visible or indirect relationships", () => {
  const data = {
    nodes: [
      node("John"),
      node("Mary"),
      { ...node("meal", "memories"), owner_entity: "John", weight: 10 },
      { ...node("walk", "memories"), owner_entity: "Mary", weight: 20 },
    ],
    links: [],
  };
  const before = structuredClone(data);
  for (const kinds of [{}, { entities: false }, { memories: false }]) {
    const view = projectGraph(data, kinds);
    assert.deepEqual(view.links, []);
    assert.equal(view.layout.links.length, 2);
    assert.ok(view.layout.links.every((link) => link.layoutOnly));
  }
  for (const dimensions of [2, 3]) {
    const distances = (ownership) => {
      const view = projectGraph(data, {});
      const nodes = structuredClone(view.layout.nodes);
      const links = structuredClone(ownership ? view.layout.links : []);
      const sim = forceSimulation(nodes, dimensions)
        .force("link", savedLinkForce(forceLink(links).id((n) => n.id)))
        .force("charge", forceManyBody())
        .force("center", forceCenter())
        .stop();
      sim.tick(120);
      return [0, 1].map((i) =>
        Math.hypot(
          ...["x", "y", ...(dimensions === 3 ? ["z"] : [])].map(
            (k) => nodes[i][k] - nodes[i + 2][k],
          ),
        ),
      );
    };
    const attracted = distances(true),
      drifting = distances(false);
    assert.ok(
      attracted.every((d, i) => d < drifting[i]),
      `${dimensions}D memories did not move closer to their owner`,
    );
  }
  assert.deepEqual(data, before);
  data.nodes[2].owner_entity = "missing";
  assert.equal(projectGraph(data, {}).layout.links.length, 1);
  data.nodes[2].owner_entity = "John";
  data.links.push(link("meal", "John"));
  assert.equal(
    projectGraph(data, {}).layout.links.length,
    2,
    "ownership doubled a saved attraction",
  );
});

test("hidden records connect visible participants without replacement points", () => {
  const data = {
    nodes: [node("John"), node("Mary"), node("meal", "memories")],
    links: [link("meal", "John"), link("meal", "Mary")],
  };
  const before = structuredClone(data),
    view = projectGraph(data, { memories: false });
  assert.deepEqual(view.nodes, [data.nodes[0], data.nodes[1]]);
  assert.deepEqual(pairs(view), ["John:Mary"]);
  assert.equal(view.links[0].indirect, true);
  assert.deepEqual(view.layout.nodes, data.nodes);
  assert.deepEqual(view.layout.links, graphLinks(data));
  assert.deepEqual(view.links[0].via, [{ id: "meal", title: "meal" }]);
  assert.deepEqual(data, before);
  assert.deepEqual(projectGraph(data, {}), {
    nodes: data.nodes,
    links: graphLinks(data),
    layout: { nodes: data.nodes, links: graphLinks(data) },
  });
  assert.deepEqual(projectGraph(data, { entities: false, memories: false }), {
    nodes: [],
    links: [],
    layout: { nodes: [], links: [] },
  });
});

test("all kind combinations bridge hidden chains and stop at visible records", () => {
  const kinds = ["entities", "memories", "topics", "stories"];
  const data = {
    nodes: kinds.map((kind, i) => node(String(i), kind)),
    links: [link("0", "1"), link("1", "2"), link("2", "3")],
  };
  for (let mask = 0; mask < 16; mask++) {
    const filters = Object.fromEntries(
      kinds.map((k, i) => [k, !!(mask & (1 << i))]),
    );
    const view = projectGraph(data, filters),
      selected = data.nodes.filter((n) => filters[n.kind]);
    assert.deepEqual(view.nodes, selected);
    assert.deepEqual(
      pairs(view),
      selected.slice(1).map((n, i) => `${selected[i].id}:${n.id}`),
    );
  }
});

test("every shared pair appears, with no arbitrary hub or neighbour limit", () => {
  const people = Array.from({ length: 40 }, (_, i) => node(`person${i}`));
  const data = {
    nodes: [...people, node("shared", "topics")],
    links: people.map((n) => link(n.id, "shared")),
  };
  const view = projectGraph(data, { topics: false });
  assert.equal(view.nodes.length, 40);
  assert.equal(view.links.length, (40 * 39) / 2);
  for (const person of people)
    assert.equal(
      view.links.filter((e) => e.source === person.id || e.target === person.id)
        .length,
      39,
    );
  const reordered = projectGraph(
    { nodes: [...data.nodes].reverse(), links: [...data.links].reverse() },
    { topics: false },
  );
  assert.deepEqual(pairs(reordered), pairs(view));
});

test("relationship types remain available as paths without inventing a new directed fact", () => {
  for (const type of [
    "related_to",
    "after",
    "caused_by",
    "part_of",
    "possibly_same_as",
  ]) {
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
    assert.deepEqual(pairs(view), ["John:Mary"]);
    assert.equal(view.links[0].type, "Indirect connection");
    assert.equal(view.links[0].indirect, true);
    assert.deepEqual(
      view.links[0].via.map((n) => n.id),
      ["a", "b"],
    );
  }
});

test("duplicate paths collapse to one line, while saved links retain precedence and types", () => {
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
  assert.equal(projectGraph(data, { memories: false }).links.length, 1);
  data.links.push(link("John", "Mary", "after"));
  assert.deepEqual(projectGraph(data, { memories: false }).links, [
    data.links.at(-1),
  ]);
});

test("hidden cycles terminate and excluded owner records cannot act as bridges", () => {
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
  assert.deepEqual(
    view.nodes.map((n) => n.id),
    ["John", "Mary", "Tom"],
  );
  assert.deepEqual(pairs(view), ["John:Mary"]);
});

test("reciprocal symmetric links display once while saved directional relationships remain", () => {
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

test("indirect lines exert zero layout influence, including saved-link degree bias, in 2D and 3D", () => {
  for (const dimensions of [2, 3]) {
    const simulate = (indirect) => {
      const nodes = [
        { id: "John", x: 0, y: 0, z: 0 },
        { id: "Mary", x: 50, y: 0, z: 20 },
        { id: "meal", x: 25, y: 45, z: 10, hidden: true },
      ];
      const byID = new Map(nodes.map((n) => [n.id, n]));
      const links = [
        { ...link("John", "meal"), hidden: true },
        { ...link("meal", "Mary"), hidden: true },
        ...(indirect ? [{ ...link("John", "Mary"), indirect: true }] : []),
      ].map((e) => ({
        ...e,
        source: byID.get(e.source),
        target: byID.get(e.target),
      }));
      const force = indirect ? savedLinkForce(forceLink()) : forceLink();
      const sim = forceSimulation(nodes, dimensions)
        .force("link", force)
        .force("charge", forceManyBody())
        .force("center", forceCenter())
        .stop();
      force.links(links);
      assert.equal(force.links().length, 2);
      sim.tick(50);
      return nodes.map(({ id, x, y, z, vx, vy, vz }) => ({
        id,
        x,
        y,
        z,
        vx,
        vy,
        vz,
      }));
    };
    assert.deepEqual(simulate(true), simulate(false));
  }
});
