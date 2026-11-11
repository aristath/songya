export const symmetricRelation = (type) =>
  type === "related_to" || type === "possibly_same_as";

// Reciprocal symmetric relationships are one displayed connection.
export function graphLinks(data) {
  const ids = new Set(data.nodes.map((node) => node.id));
  const links = new Map();
  for (const link of data.links) {
    let { source, target, type } = link;
    if (!ids.has(source) || !ids.has(target)) continue;
    if (symmetricRelation(type) && source > target)
      [source, target] = [target, source];
    links.set(JSON.stringify([source, target, type]), {
      ...link,
      source,
      target,
    });
  }
  return [...links.values()];
}

// Visible points stop a path. Hidden points carry it but never appear as nodes.
export function projectGraph(data, kinds) {
  const nodes = data.nodes.filter((node) => kinds[node.kind] !== false);
  if (!nodes.length)
    return { nodes, links: [], layout: { nodes: [], links: [] } };
  const visible = new Set(nodes.map((node) => node.id));
  const links = graphLinks(data);
  const result = links.filter(
    (link) => visible.has(link.source) && visible.has(link.target),
  );
  const pair = (a, b) => JSON.stringify([a, b].sort());
  const connected = new Set(
    result.map((link) => pair(link.source, link.target)),
  );
  const adjacency = new Map(data.nodes.map((node) => [node.id, new Set()]));
  for (const { source, target } of links) {
    if (source === target) continue;
    adjacency.get(source).add(target);
    adjacency.get(target).add(source);
  }
  const neighbours = new Map(
    [...adjacency].map(([id, peers]) => [id, [...peers].sort()]),
  );
  const records = new Map(data.nodes.map((node) => [node.id, node]));
  for (const source of visible) {
    const parents = new Map([[source, null]]);
    const queue = [source];
    for (const from of queue) {
      for (const target of neighbours.get(from)) {
        if (visible.has(target)) {
          if (
            from === source ||
            target === source ||
            connected.has(pair(source, target))
          )
            continue;
          const via = [];
          for (let id = from; id !== source; id = parents.get(id))
            via.push({ id, title: records.get(id).title || id });
          via.reverse();
          const [a, b] = [source, target].sort();
          if (a !== source) via.reverse();
          result.push({
            source: a,
            target: b,
            type: "Indirect connection",
            indirect: true,
            via,
          });
          connected.add(pair(source, target));
        } else if (!parents.has(target)) {
          parents.set(target, from);
          queue.push(target);
        }
      }
    }
  }
  // Ownership attracts memories to their user's identity for positioning only.
  // These connections never enter visible paths, scores, or saved relationships.
  const ownership = data.nodes
    .filter(
      (node) => node.kind === "memories" && records.has(node.owner_entity),
    )
    .filter(
      (node) =>
        !links.some(
          (link) =>
            pair(link.source, link.target) === pair(node.id, node.owner_entity),
        ),
    )
    .map((node) => ({
      source: node.id,
      target: node.owner_entity,
      layoutOnly: true,
    }));
  return {
    nodes,
    links: result,
    layout: { nodes: data.nodes, links: [...links, ...ownership] },
  };
}

// Exclude indirect links entirely: zero strength would still change D3's degree
// counts and therefore change the forces exerted by saved links.
export function savedLinkForce(force) {
  const links = force.links;
  force.links = function (value) {
    if (!arguments.length) return links.call(force);
    links.call(
      force,
      value.filter((link) => !link.indirect),
    );
    return force;
  };
  return force;
}
