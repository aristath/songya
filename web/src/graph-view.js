// Contract hidden points into weighted connections between the points left on
// screen. A broadly shared record spreads its influence across its neighbours;
// separate paths add support. Visible points always stop that contraction.
export function projectGraph(data, kinds) {
  const visible = data.nodes.filter((node) => kinds[node.kind] !== false);
  const adjacency = new Map(data.nodes.map((node) => [node.id, new Map()]));
  const links = data.links.filter(
    (link) => adjacency.has(link.source) && adjacency.has(link.target),
  );
  if (visible.length === data.nodes.length || !visible.length)
    return { nodes: visible, links: visible.length ? links : [] };

  for (const { source, target } of links) {
    if (source === target) continue;
    // Reciprocal links and different relationship types are still one adjacency.
    adjacency.get(source).set(target, 1);
    adjacency.get(target).set(source, 1);
  }
  const ids = new Set(visible.map((node) => node.id));
  const hidden = data.nodes
    .filter((node) => !ids.has(node.id))
    .sort(
      (a, b) =>
        adjacency.get(a.id).size - adjacency.get(b.id).size ||
        a.id.localeCompare(b.id),
    );
  for (const { id } of hidden) {
    const neighbours = [...adjacency.get(id)];
    const total = neighbours.reduce((sum, [, strength]) => sum + strength, 0);
    for (let i = 0; i < neighbours.length; i++) {
      const [a, first] = neighbours[i];
      adjacency.get(a).delete(id);
      for (let j = i + 1; j < neighbours.length; j++) {
        const [b, second] = neighbours[j];
        const strength =
          (adjacency.get(a).get(b) || 0) + (first * second) / total;
        adjacency.get(a).set(b, strength);
        adjacency.get(b).set(a, strength);
      }
    }
    adjacency.delete(id);
  }

  const projected = links.filter(
    (link) => ids.has(link.source) && ids.has(link.target),
  );
  const direct = new Map(visible.map((node) => [node.id, new Set()]));
  for (const { source, target } of projected) {
    direct.get(source).add(target);
    direct.get(target).add(source);
  }
  const candidates = [];
  for (const [source, neighbours] of adjacency) {
    for (const [target, strength] of neighbours) {
      if (source >= target || direct.get(source).has(target)) continue;
      candidates.push({
        source,
        target,
        type: "Connected through hidden records",
        projected: true,
        strength,
      });
    }
  }
  // A hidden hub can connect hundreds of points. Keep the strongest local
  // neighbours and a maximum-strength connecting forest, not that whole clique.
  const parents = new Map(visible.map((node) => [node.id, node.id]));
  const root = (id) => {
    while (parents.get(id) !== id) {
      parents.set(id, parents.get(parents.get(id)));
      id = parents.get(id);
    }
    return id;
  };
  const join = (a, b) => {
    a = root(a);
    b = root(b);
    if (a === b) return false;
    parents.set(a, b);
    return true;
  };
  for (const link of projected) join(link.source, link.target);
  const counts = new Map(visible.map((node) => [node.id, 0]));
  candidates.sort((a, b) => b.strength - a.strength);
  for (const link of candidates) {
    const bridge = join(link.source, link.target);
    if (bridge || counts.get(link.source) < 3 || counts.get(link.target) < 3) {
      projected.push(link);
      counts.set(link.source, counts.get(link.source) + 1);
      counts.set(link.target, counts.get(link.target) + 1);
    }
  }
  return { nodes: visible, links: projected };
}
