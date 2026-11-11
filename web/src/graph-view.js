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

// Hidden records remain neutral junctions in their original relationships.
// Remove hidden dead ends: they do not connect anything left on screen.
export function projectGraph(data, kinds) {
  const visible = new Set(
    data.nodes
      .filter((node) => kinds[node.kind] !== false)
      .map((node) => node.id),
  );
  if (!visible.size) return { nodes: [], links: [] };
  const links = graphLinks(data);
  const adjacency = new Map(data.nodes.map((node) => [node.id, new Set()]));
  for (const { source, target } of links) {
    if (source === target) continue;
    adjacency.get(source).add(target);
    adjacency.get(target).add(source);
  }
  const leaves = [...adjacency.keys()].filter(
    (id) => !visible.has(id) && adjacency.get(id).size < 2,
  );
  for (const id of leaves) {
    for (const neighbour of adjacency.get(id)) {
      adjacency.get(neighbour).delete(id);
      if (!visible.has(neighbour) && adjacency.get(neighbour).size === 1)
        leaves.push(neighbour);
    }
    adjacency.delete(id);
  }
  // Entirely hidden components have no place in the selected view.
  const connected = new Set(visible);
  for (const id of connected)
    for (const neighbour of adjacency.get(id) || []) connected.add(neighbour);
  return {
    nodes: data.nodes
      .filter((node) => adjacency.has(node.id) && connected.has(node.id))
      .map((node) => ({ ...node, junction: !visible.has(node.id) })),
    links: links.filter(
      (link) => connected.has(link.source) && connected.has(link.target),
    ),
  };
}
