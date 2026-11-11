import ForceGraph3D from "3d-force-graph";
import ForceGraph from "force-graph";
import { forceCollide } from "d3-force-3d";
import { symmetricRelation, savedLinkForce } from "./graph-view.js";
import { CanvasTexture, Sprite, SpriteMaterial, SRGBColorSpace } from "three";
import { portraitURL, paintPortrait } from "./portraits.js";

import { currentPalette, resolveColor, defaultKindColors } from "./palette.js";

// Labels are DOM text: record names and relationship types may contain markup.
function label(text) {
  const element = document.createElement("span");
  element.textContent = text;
  return element;
}

export function memoryGraph(
  container,
  inspect,
  mode = "2d",
  nodeColor = (node) => node.color || defaultKindColors[node.kind],
) {
  let weightUnit = 1,
    lineDensity = 1,
    hovered = null,
    selected = null;
  const nodeValue = (node) =>
    node.hidden ? 0.05 : 1 + Math.max(0, Number(node.weight) || 0) / weightUnit;
  const nodeRadius = (node) => 4 * Math.sqrt(nodeValue(node));
  let colors = currentPalette();
  const flat = mode === "2d";
  const fitPadding = () =>
    Math.max(
      36,
      (container.parentElement.querySelector(".graph-toolbar")?.offsetHeight ||
        0) + 24,
      (container.parentElement.querySelector(".graph-legend")?.offsetHeight ||
        0) + 24,
    );
  let fit = true,
    matches = null,
    layoutStamp = "";
  let destroyed = false;
  const pictures = new Map(),
    sprites = new Set();
  const imageFor = (node) => {
    if (node.hidden || !node.profile_picture) return null;
    const url = portraitURL(node.id, node.profile_picture, node.revision);
    if (!pictures.has(url)) {
      const image = new Image();
      pictures.set(url, image);
      image.onload = () => {
        if (!destroyed) refreshPictures();
      };
      image.onerror = () => {
        image.retry = setTimeout(() => {
          if (destroyed || pictures.get(url) !== image) return;
          pictures.delete(url);
          refreshPictures();
        }, 30000);
      };
      image.src = url;
    }
    const image = pictures.get(url);
    return image.complete && image.naturalWidth ? image : null;
  };
  const disposeSprites = () => {
    for (const sprite of sprites) {
      sprite.material.map?.dispose();
      sprite.material.dispose();
    }
    sprites.clear();
  };
  const graph = new (flat ? ForceGraph : ForceGraph3D)(container)
    .backgroundColor(colors.crust)
    .nodeColor((node) =>
      matches && !matches.has(node.id)
        ? colors.surface1
        : resolveColor(nodeColor(node), colors),
    )
    .nodeVal(nodeValue)
    .nodeVisibility((node) => !node.hidden)
    .linkVisibility((link) => !link.hidden)
    .nodeLabel((node) => label(node.title || node.id))
    .linkColor((link) => {
      const ids = [
        link.source.id || link.source,
        link.target.id || link.target,
      ];
      const highlighted = ids.some((id) => id === hovered || id === selected);
      const active =
        highlighted ||
        !matches ||
        matches.has(link.source.id || link.source) ||
        matches.has(link.target.id || link.target);
      const color = active ? colors.overlay1 : colors.surface0;
      const opacity =
        // Two alpha steps keep half-opacity indirect lines visible in 8-bit canvas.
        (highlighted
          ? 0.7
          : Math.max(2 / 255, (flat ? 0.5 : 0.35) / lineDensity)) *
        (link.indirect ? 0.5 : 1);
      const rgb = [1, 3, 5].map((offset) =>
        parseInt(color.slice(offset, offset + 2), 16),
      );
      return `rgba(${rgb.join(",")},${opacity})`;
    })
    .linkLabel((link) =>
      label(
        [
          link.type,
          ...(link.via || []).map((record) => `${record.title} (${record.id})`),
        ].join(" · "),
      ),
    )
    .linkDirectionalArrowLength((link) =>
      link.indirect || symmetricRelation(link.type) ? 0 : 2,
    )
    .onNodeHover((node) => {
      const next = node?.id || null;
      if (hovered === next) return;
      hovered = next;
      graph.linkColor(graph.linkColor());
    })
    .onNodeClick((node) => {
      selected = node.id;
      graph.linkColor(graph.linkColor());
      inspect(node.id);
    })
    .onBackgroundClick(() => {
      selected = null;
      graph.linkColor(graph.linkColor());
    })
    .cooldownTicks(120)
    .onEngineStop(() => {
      if (fit && graph.graphData().nodes.some((node) => !node.hidden)) {
        graph.zoomToFit(400, fitPadding(), (node) => !node.hidden);
        fit = false;
      }
    });
  graph.d3Force("link", savedLinkForce(graph.d3Force("link")));
  if (flat) {
    graph
      .nodeCanvasObjectMode(() => "after")
      .nodeCanvasObject((node, ctx) => {
        const image = imageFor(node);
        if (!image) return;
        const radius = nodeRadius(node);
        ctx.save();
        if (matches && !matches.has(node.id)) ctx.globalAlpha = 0.2;
        paintPortrait(
          ctx,
          image,
          node.profile_picture,
          node.x - radius,
          node.y - radius,
          radius * 2,
        );
        ctx.strokeStyle = resolveColor(nodeColor(node), colors);
        ctx.lineWidth = 1.5;
        ctx.beginPath();
        ctx.arc(node.x, node.y, radius, 0, 2 * Math.PI);
        ctx.stroke();
        ctx.restore();
      });
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
  } else {
    graph
      .showNavInfo(false)
      .nodeOpacity(1)
      .linkOpacity(1)
      .nodeThreeObject((node) => {
        const image = imageFor(node);
        if (!image) return undefined;
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 256;
        const ctx = canvas.getContext("2d");
        paintPortrait(ctx, image, node.profile_picture, 4, 4, 248);
        ctx.strokeStyle = resolveColor(nodeColor(node), colors);
        ctx.lineWidth = 8;
        ctx.beginPath();
        ctx.arc(128, 128, 124, 0, Math.PI * 2);
        ctx.stroke();
        const texture = new CanvasTexture(canvas);
        texture.colorSpace = SRGBColorSpace;
        const sprite = new Sprite(
          new SpriteMaterial({
            map: texture,
            transparent: true,
            opacity: matches && !matches.has(node.id) ? 0.2 : 1,
          }),
        );
        const diameter = 8 * Math.cbrt(nodeValue(node));
        sprite.scale.set(diameter, diameter, 1);
        sprites.add(sprite);
        return sprite;
      });
  }
  const refreshPictures = () => {
    if (!flat) {
      disposeSprites();
      graph.nodeThreeObject(graph.nodeThreeObject());
    }
  };
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
    refreshPictures();
  };
  const paletteTimer = setInterval(restyle, 500);
  const resize = new ResizeObserver(() => {
    graph.width(container.clientWidth).height(container.clientHeight);
    if (!fit) graph.zoomToFit(0, fitPadding(), (node) => !node.hidden);
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
      refreshPictures();
    },
    focus(id) {
      selected = id || null;
      graph.linkColor(graph.linkColor());
      if (!id) {
        // Finish after an earlier focus transition when search is cleared quickly.
        graph.zoomToFit(650, fitPadding(), (node) => !node.hidden);
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
        const distance = 100 + Math.cbrt(nodeValue(node)) * 12;
        graph.cameraPosition({ x, y, z: z + distance }, { x, y, z }, 650);
      }
    },
    destroy() {
      destroyed = true;
      disposeSprites();
      for (const image of pictures.values()) {
        image.onload = image.onerror = null;
        clearTimeout(image.retry);
      }
      pictures.clear();
      clearInterval(paletteTimer);
      resize.disconnect();
      visibility.disconnect();
      const renderer = flat ? null : graph.renderer();
      graph._destructor();
      renderer?.forceContextLoss();
    },
    update(data, refit = false) {
      // Both renderers mutate data. Give each its own nodes and links.
      const previous = new Map(
        graph.graphData().nodes.map((node) => [node.id, node]),
      );
      const visibleIDs = new Set(data.nodes.map((node) => node.id));
      const layout = data.layout;
      const nodes = layout.nodes.map((node) => {
        const copy = { ...node, hidden: !visibleIDs.has(node.id) };
        const old = previous.get(node.id);
        if (old) {
          for (const key of ["x", "y", "z", "vx", "vy", "vz", "fx", "fy", "fz"])
            if (key in old) copy[key] = old[key];
        }
        return copy;
      });
      const previousUnit = weightUnit;
      const visible = nodes.filter((node) => !node.hidden);
      weightUnit =
        visible.reduce(
          (sum, node) => sum + Math.max(0, Number(node.weight) || 0),
          0,
        ) / visible.length || 1;
      const byID = new Map(nodes.map((node) => [node.id, node]));
      if (!visibleIDs.has(hovered)) hovered = null;
      if (!visibleIDs.has(selected)) selected = null;
      const links = [
        ...layout.links.filter((link) => !link.indirect),
        ...data.links.filter((link) => link.indirect),
      ]
        .filter((link) => byID.has(link.source) && byID.has(link.target))
        .map((link) => ({
          ...link,
          hidden: !visibleIDs.has(link.source) || !visibleIDs.has(link.target),
        }));
      lineDensity = Math.max(
        1,
        (2 * data.links.length) / (data.nodes.length || 1),
      );
      const stamp = JSON.stringify([
        nodes.map((node) => node.id).sort(),
        links
          .filter((link) => !link.indirect)
          .map((link) => JSON.stringify([link.source, link.target, link.type]))
          .sort(),
      ]);
      if (stamp === layoutStamp) {
        // Color and label changes do not need a new force layout or camera fit.
        let sizeChanged = previousUnit !== weightUnit;
        for (const node of graph.graphData().nodes) {
          sizeChanged ||=
            node.weight !== byID.get(node.id)?.weight ||
            node.hidden !== byID.get(node.id)?.hidden;
          Object.assign(node, byID.get(node.id));
        }
        const current = graph.graphData();
        const currentNodes = new Map(
          current.nodes.map((node) => [node.id, node]),
        );
        // Redraw visual connections without reheating the physical graph.
        current.links = links.map((link) => ({
          ...link,
          source: currentNodes.get(link.source),
          target: currentNodes.get(link.target),
        }));
        graph
          .nodeVal(nodeValue)
          .nodeColor(graph.nodeColor())
          .nodeVisibility(graph.nodeVisibility())
          .linkVisibility(graph.linkVisibility())
          .nodeLabel(graph.nodeLabel())
          .linkColor(graph.linkColor());
        if (sizeChanged && flat) {
          graph.d3Force("collide").radius((node) => nodeRadius(node) + 4);
          graph
            .d3Force("link")
            .distance(
              (link) => nodeRadius(link.source) + nodeRadius(link.target) + 28,
            );
        }
        if (refit) {
          if (sizeChanged) {
            fit = true;
            graph.d3ReheatSimulation();
          } else {
            graph.zoomToFit(400, fitPadding(), (node) => !node.hidden);
            fit = false;
          }
        }
        refreshPictures();
        return;
      }
      fit ||= refit || !previous.size;
      layoutStamp = stamp;
      disposeSprites();
      // Indirect endpoints need resolving too, even though they skip the force.
      for (const link of links) {
        link.source = byID.get(link.source);
        link.target = byID.get(link.target);
      }
      graph.graphData({ nodes, links });
      const active = new Set(
        nodes
          .filter((n) => n.profile_picture)
          .map((n) => portraitURL(n.id, n.profile_picture, n.revision)),
      );
      for (const [url, image] of pictures)
        if (!active.has(url)) {
          image.onload = null;
          image.onerror = null;
          clearTimeout(image.retry);
          pictures.delete(url);
        }
    },
  };
}
