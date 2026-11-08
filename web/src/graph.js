import ForceGraph3D from "3d-force-graph";
import ForceGraph from "force-graph";
import { forceCollide } from "d3-force-3d";

import { currentPalette, resolveColor, defaultKindColors } from "./palette.js";

// Labels are DOM text: record names and relationship types may contain markup.
function label(text) {
  const element = document.createElement("span");
  element.textContent = text;
  return element;
}

const nodeRadius = (node) => 4 * Math.sqrt(1 + node.connections);

export function memoryGraph(
  container,
  inspect,
  mode = "2d",
  nodeColor = (node) => node.color || defaultKindColors[node.kind],
) {
  let colors = currentPalette();
  const flat = mode === "2d";
  const fitPadding = () =>
    Math.max(
      36,
      (container.parentElement.querySelector(".graph-toolbar")?.offsetHeight ||
        0) + 24,
    );
  let fit = true,
    matches = null,
    layoutStamp = "";
  const graph = new (flat ? ForceGraph : ForceGraph3D)(container)
    .backgroundColor(colors.crust)
    .nodeColor((node) =>
      matches && !matches.has(node.id)
        ? colors.surface1
        : resolveColor(nodeColor(node), colors),
    )
    .nodeVal((node) => 1 + node.connections)
    .nodeLabel((node) => label(node.title || node.id))
    .linkColor((link) =>
      !matches ||
      matches.has(link.source.id || link.source) ||
      matches.has(link.target.id || link.target)
        ? flat
          ? colors.overlay1 + "80"
          : colors.overlay1
        : colors.surface0,
    )
    .linkLabel((link) => label(link.type))
    .linkDirectionalArrowLength(2)
    .onNodeClick((node) => inspect(node.id))
    .cooldownTicks(120)
    .onEngineStop(() => {
      if (fit && graph.graphData().nodes.length) {
        graph.zoomToFit(400, fitPadding());
        fit = false;
      }
    });
  if (flat) {
    graph
      .warmupTicks(80)
      .d3Force(
        "collide",
        forceCollide((node) => nodeRadius(node) + 4).iterations(3),
      );
    graph
      .d3Force("link")
      .distance(
        (link) => nodeRadius(link.source) + nodeRadius(link.target) + 28,
      );
  } else graph.showNavInfo(false).nodeOpacity(1).linkOpacity(0.35);
  // Read the native CSS theme; no viewport queries or layout breakpoints.
  // The interval also catches device-theme changes without replacing graph data.
  const restyle = () => {
    const next = currentPalette();
    if (next === colors) return;
    colors = next;
    graph
      .backgroundColor(colors.crust)
      .nodeColor(graph.nodeColor())
      .linkColor(graph.linkColor());
  };
  const paletteTimer = setInterval(restyle, 500);
  const resize = new ResizeObserver(() => {
    graph.width(container.clientWidth).height(container.clientHeight);
    if (!fit) graph.zoomToFit(0, fitPadding());
  });
  resize.observe(container);
  const visibility = new IntersectionObserver(([entry]) => {
    if (entry.isIntersecting) graph.resumeAnimation();
    else graph.pauseAnimation();
  });
  visibility.observe(container);
  return {
    restyle,
    highlight(ids) {
      matches = ids === null ? null : new Set(ids);
      graph.nodeColor(graph.nodeColor()).linkColor(graph.linkColor());
    },
    focus(id) {
      if (!id) {
        graph.zoomToFit(500, fitPadding());
        return;
      }
      const node = graph.graphData().nodes.find((node) => node.id === id);
      if (!node) return;
      fit = false;
      const { x = 0, y = 0, z = 0 } = node;
      if (flat) {
        const radius = nodeRadius(node);
        const zoom = Math.min(
          3,
          Math.min(container.clientWidth, container.clientHeight) /
            (radius * 6),
        );
        graph.centerAt(x, y, 650).zoom(zoom, 650);
      } else {
        const distance = 100 + Math.cbrt(1 + node.connections) * 12;
        graph.cameraPosition({ x, y, z: z + distance }, { x, y, z }, 650);
      }
    },
    destroy() {
      clearInterval(paletteTimer);
      resize.disconnect();
      visibility.disconnect();
      const renderer = flat ? null : graph.renderer();
      graph._destructor();
      renderer?.forceContextLoss();
    },
    update(data) {
      // Both renderers mutate data. Give each its own nodes and links.
      const nodes = data.nodes.map((node) => ({ ...node, connections: 0 }));
      const byID = new Map(nodes.map((node) => [node.id, node]));
      const links = data.links
        .filter((link) => byID.has(link.source) && byID.has(link.target))
        .map((link) => ({ ...link }));
      for (const link of links) {
        byID.get(link.source).connections++;
        byID.get(link.target).connections++;
      }
      const stamp = JSON.stringify([
        nodes.map((node) => node.id),
        links.map((link) => [link.source, link.target, link.type]),
      ]);
      if (stamp === layoutStamp) {
        // Color and label changes do not need a new force layout or camera fit.
        for (const node of graph.graphData().nodes)
          Object.assign(node, byID.get(node.id));
        graph.nodeColor(graph.nodeColor()).nodeLabel(graph.nodeLabel());
        return;
      }
      layoutStamp = stamp;
      fit = true;
      graph.graphData({ nodes, links });
    },
  };
}
