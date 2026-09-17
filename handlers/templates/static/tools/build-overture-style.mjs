#!/usr/bin/env node
//
// Assembles the Overture Maps "explore" basemap cartography into a single
// MapLibre style, written to overture-style.json (which index.js bundles).
//
// The upstream layers live in OvertureMaps/explore-site as ~100 per-layer JSON
// files whose paint/layout values are design tokens ("$base.water.ocean.color.fill")
// resolved at build time against a three-tier token set. This mirrors the
// resolution done by explore-site's components/map/index.js.
//
// Run with: npm run build:overture-style
//
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

// Pinned so regenerating the style is reproducible. Bump to pick up upstream
// cartography changes, then re-run and review the diff.
const REPO = "OvertureMaps/explore-site";
const COMMIT = "51e240fc7e5db326324429169e461c0ff8a300ab";

// Themes whose layers are dropped: they are point-dense overlays rather than
// basemap context, and they are the only layers needing a sprite sheet.
const EXCLUDED_SOURCES = new Set(["places", "addresses"]);

// Glyph host serving Noto Sans as SDF PBFs. Upstream renders labels from woff2
// fonts loaded in the browser, which needs maplibre-gl v5+; we bundle v4, so
// font stacks are remapped onto what this host actually serves.
const GLYPHS = "https://protomaps.github.io/basemaps-assets/fonts/{fontstack}/{range}.pbf";
const AVAILABLE_FONTS = new Set(["Noto Sans Regular", "Noto Sans Medium", "Noto Sans Italic"]);
// --- CARTO Positron-style palette -------------------------------------------
//
// Overture's explore cartography is a data showcase: saturated greens, a blue
// ocean, tan land. Under a tileset preview the overlay should be the loudest
// thing on screen, so the palette below restyles it after CARTO's Positron
// basemap - near-white land, flat cool-gray water, white roads with light gray
// casings, blue-gray labels. Values sampled from
// https://basemaps.cartocdn.com/gl/positron-gl-style/style.json
//
// Colors are Positron's darkened ~14% so the basemap sits less stark
// behind the overlay.
//
// These replace Overture's semantic color tokens, which every layer resolves
// against, so the recolor reaches all 81 layers. Set to {} for Overture's own
// colors.
const FLAT_WATER = "#b8bcbe";
const PALETTE = {
  background: "#d8d8d6",
  land: "#d8d8d6",
  ocean: FLAT_WATER,
  "lake-river": FLAT_WATER,
  "water-label": "#69828b",
  // Positron draws water flat, so Overture's depth ramp collapses to one color.
  "bathymetry-0": FLAT_WATER,
  "bathymetry-100": FLAT_WATER,
  "bathymetry-500": FLAT_WATER,
  "bathymetry-1000": FLAT_WATER,
  "bathymetry-1500": FLAT_WATER,
  "bathymetry-2000": FLAT_WATER,
  "bathymetry-2500": FLAT_WATER,
  "bathymetry-3000": FLAT_WATER,
  // Positron's landcover is a single barely-there green; Overture splits it by
  // surface type, so the types collapse onto near-neutral variants of it.
  barren: "#d0d0cc",
  forest: "#cad0ca",
  grass: "#cdd1cc",
  moss: "#cad0ca",
  shrub: "#cdd1cc",
  crop: "#d0d1cc",
  wetland: "#cacecf",
  mangrove: "#cacecf",
  snow: "#d4d5d5",
  park: { primary: "#cad0ca", highlight: "#c3cbc2" },
  recreation: { primary: "#cfd4ce", highlight: "#c9cec8" },
  medical: { primary: "#d3d0d0", highlight: "#cac6c6" },
  "college-university": { primary: "#d2d1cd", highlight: "#cac8c2" },
  military: { primary: "#d1d1d0", highlight: "#c9c9c7" },
  cemetery: { primary: "#ced0cd", highlight: "#c7cac5" },
  airport: { primary: "#cdcfd0", highlight: "#c4c7c9" },
  aerialway: { primary: "#cdcfd0", highlight: "#c1c4c6" },
  pier: { primary: "#d0d0ce", highlight: "#c7c7c4" },
  building: { primary: "#cdcdcd", highlight: "#c1c1c1", tertiary: "#c5c5c5" },
  "building-part": { primary: "#c9c9c9", highlight: "#bcbcbc", tertiary: "#c1c1c1" },
  // Streets stay white rather than darkening with the rest of the palette, so
  // they read against both the gray land and the lighter DVRPC region.
  segment: { primary: "#ffffff", highlight: "#c7c7c7" },
  motorway: { primary: "#ffffff", highlight: "#bfbfbf", label: "#717171", labelHalo: "rgba(220, 220, 220, 0.9)" },
  road: { primary: "#fdfdfd", highlight: "#c7c7c7", label: "#717171", labelHalo: "rgba(220, 220, 220, 0.9)" },
  pedestrian: "rgba(184, 184, 184, 0.7)",
  rail: { primary: "rgba(191, 191, 191, 0.9)", label: "#717171", labelHalo: "rgba(220, 220, 220, 0.9)" },
  waterway: { primary: "rgba(181, 189, 193, 0.7)", label: "#69828b", labelHalo: "#d4d4d2" },
  division: {
    boundary: { primary: "#beb6b7", highlight: "#dcdcdc" },
    label: "#5b6a77",
    halo: "rgba(220, 220, 220, 0.75)",
  },
};

// Division labels fade from their label color toward this primitive as you zoom
// in, so it has to move with the palette.
const PRIMITIVE_OVERRIDES = {
  "color.gray.600": "#859099",
};

// Per-layer patches applied after token resolution, deep-merged onto the layer.
const LAYER_OVERRIDES = {
  // Upstream draws every locality at every zoom, which crowds small scales.
  // Gate them on @prominence, relaxing the threshold as you zoom in, and give
  // each label more collision padding.
  "divisions-division-locality-label": {
    filter: [
      "all",
      ["match", ["get", "subtype"], ["locality"], true, false],
      [">=", ["coalesce", ["get", "@prominence"], 0], ["step", ["zoom"], 78, 9, 62, 11, 42, 13, 0]],
    ],
    layout: { "text-padding": 8 },
  },
  // Townships, boroughs and neighborhoods: too fine-grained to read before z11.
  "divisions-division-localadmin-local-label": {
    minzoom: 11,
    layout: { "text-padding": 6 },
  },
  "divisions-division-county-label": {
    layout: { "text-padding": 6 },
  },
};

// Overture sizes labels for a full-screen map browser; over a tileset preview
// the same sizes crowd the overlay, so the division labels are scaled down.
// Scaling is applied to the output values of the size expression, keeping
// upstream's relative hierarchy (country > region > county > town) intact.
const LABEL_SIZE_SCALE = {
  "divisions-division-country-label": 0.65,
  "divisions-division-region-label": 0.65,
  "divisions-division-county-label": 0.65,
  "divisions-division-locality-label": 0.65,
  "divisions-division-localadmin-local-label": 0.65,
};

// The nine-county DVRPC region is drawn lighter than the surrounding land so it
// reads as the focus area. Matched on Overture's region code and county name;
// division_area carries counties from z8, so below that the region is not
// distinguished. Clipped to land so the Delaware Bay shoreline stays crisp.
const DVRPC_COUNTIES = [
  "US-PA|Bucks County",
  "US-PA|Chester County",
  "US-PA|Delaware County",
  "US-PA|Montgomery County",
  "US-PA|Philadelphia County",
  "US-NJ|Burlington County",
  "US-NJ|Camden County",
  "US-NJ|Gloucester County",
  "US-NJ|Mercer County",
];

// Extra layers inserted directly after the named upstream layer. An entry with
// copyOf clones an earlier layer under a new id, to redraw it higher up.
const EXTRA_LAYERS = {
  "base-land-fill": [
    {
      id: "dvrpc-region-fill",
      type: "fill",
      source: "divisions",
      "source-layer": "division_area",
      minzoom: 8,
      filter: [
        "all",
        ["==", ["get", "subtype"], "county"],
        ["==", ["get", "class"], "land"],
        ["match", ["concat", ["get", "region"], "|", ["get", "@name"]], DVRPC_COUNTIES, true, false],
      ],
      paint: {
        "fill-color": "#fafaf8",
        "fill-outline-color": "hsla(0, 0%, 0%, 0)",
      },
    },
    // County areas extend into the Delaware estuary, which Overture tags as
    // ocean and draws beneath the land; redraw it above the region fill.
    { id: "dvrpc-region-ocean-fill", copyOf: "base-water-ocean-fill" },
  ],
};

const FONT_SUBSTITUTIONS = {
  "Noto Sans SemiCondensed Regular": "Noto Sans Regular",
  "Noto Sans SemiCondensed Light": "Noto Sans Regular",
  "Noto Sans SemiCondensed Medium": "Noto Sans Medium",
  "Noto Sans SemiCondensed Bold": "Noto Sans Medium",
  "Noto Sans Bold": "Noto Sans Medium",
  "Noto Sans Light": "Noto Sans Regular",
  "PT Mono Regular": "Noto Sans Regular",
};

const outFile = resolve(dirname(fileURLToPath(import.meta.url)), "..", "overture-style.json");

function fetchUpstream() {
  const dir = mkdtempSync(join(tmpdir(), "overture-style-"));
  const tarball = join(dir, "src.tar.gz");
  execFileSync("curl", ["-sL", "--fail", "--max-time", "120",
    `https://github.com/${REPO}/archive/${COMMIT}.tar.gz`, "-o", tarball]);
  execFileSync("tar", ["xzf", tarball, "-C", dir]);
  return { dir, mapDir: join(dir, `explore-site-${COMMIT}`, "components", "map") };
}

const { dir: tmpDir, mapDir } = fetchUpstream();
const read = (p) => JSON.parse(readFileSync(join(mapDir, p), "utf8"));

const primitives = {
  color: read("tokens/primitive/colors.json"),
  font: read("tokens/primitive/fonts.json"),
};

for (const [path, value] of Object.entries(PRIMITIVE_OVERRIDES)) {
  const parts = path.split(".");
  const target = parts.slice(0, -1).reduce((acc, part) => acc?.[part], primitives);
  if (!target) throw new Error(`primitive override path not found: ${path}`);
  target[parts[parts.length - 1]] = value;
}

function merge(base, override) {
  if (!override) return base;
  const result = { ...base };
  for (const key of Object.keys(override)) {
    const a = result[key];
    const b = override[key];
    result[key] = a && b && typeof a === "object" && typeof b === "object"
      && !Array.isArray(a) && !Array.isArray(b) ? merge(a, b) : b;
  }
  return result;
}

function lookup(root, path) {
  let val = root;
  for (const part of path) {
    val = val?.[part];
    if (val === undefined) return undefined;
  }
  return val;
}

// Pass 1: semantic tokens reference primitives as "$globals.color.blue.600".
function resolveGlobals(val) {
  if (typeof val === "string" && val.startsWith("$globals.")) {
    const resolved = lookup(primitives, val.slice("$globals.".length).split("."));
    return resolved === undefined ? val : resolved;
  }
  if (Array.isArray(val)) return val.map(resolveGlobals);
  if (val && typeof val === "object") {
    return Object.fromEntries(Object.entries(val).map(([k, v]) => [k, resolveGlobals(v)]));
  }
  return val;
}

const upstreamColors = resolveGlobals(read("tokens/semantic/explore/colors.json"));
for (const key of Object.keys(PALETTE)) {
  if (!(key in upstreamColors)) throw new Error(`palette key is not an Overture color token: ${key}`);
}
const semanticColors = merge(upstreamColors, PALETTE);
const semanticFonts = resolveGlobals(read("tokens/semantic/fonts.json"));

// Pass 2: component tokens reference semantic tokens and primitives.
function resolveComponent(val) {
  if (typeof val === "string" && val.startsWith("$semantic.")) {
    const resolved = lookup(semanticColors, val.slice("$semantic.".length).split("."));
    return resolved === undefined ? val : resolved;
  }
  if (typeof val === "string" && val.startsWith("$globals.")) {
    const resolved = lookup(primitives, val.slice("$globals.".length).split("."));
    return resolved === undefined ? val : resolved;
  }
  if (Array.isArray(val)) return val.map(resolveComponent);
  if (val && typeof val === "object") {
    return Object.fromEntries(Object.entries(val).map(([k, v]) => [k, resolveComponent(v)]));
  }
  return val;
}

const themeTokens = merge(resolveComponent(read("tokens/component/explore.json")), semanticFonts);

// Pass 3: layer specs reference component tokens as "$base.water.ocean.color.fill".
function resolveLayerSpec(spec) {
  const unresolved = [];
  const json = JSON.stringify(spec).replace(/"\$([^"]+)"/g, (match, path) => {
    const parts = path.split(".");
    const root = parts[0] === "globals" ? primitives : themeTokens;
    const val = lookup(root, parts[0] === "globals" ? parts.slice(1) : parts);
    if (val === undefined) {
      unresolved.push(path);
      return match;
    }
    return JSON.stringify(val);
  });
  return { layer: JSON.parse(json), unresolved };
}

function substituteFonts(val, replaced) {
  if (typeof val === "string" && (AVAILABLE_FONTS.has(val) || FONT_SUBSTITUTIONS[val])) {
    const sub = AVAILABLE_FONTS.has(val) ? val : FONT_SUBSTITUTIONS[val];
    if (sub !== val) replaced.add(`${val} -> ${sub}`);
    return sub;
  }
  if (Array.isArray(val)) return val.map((v) => substituteFonts(v, replaced));
  if (val && typeof val === "object") {
    return Object.fromEntries(Object.entries(val).map(([k, v]) => [k, substituteFonts(v, replaced)]));
  }
  return val;
}

// Scales the output values of a size expression, leaving inputs (zoom stops,
// match labels, conditions) untouched. Throws on shapes it does not understand
// rather than silently leaving a layer unscaled.
function scaleSize(value, scale) {
  if (typeof value === "number") return Math.round(value * scale * 10) / 10;
  if (!Array.isArray(value)) throw new Error(`cannot scale size: ${JSON.stringify(value)}`);

  const [op] = value;
  const scaled = value.slice();
  const scaleAt = (indices) => indices.forEach((i) => { scaled[i] = scaleSize(value[i], scale); });

  if (op === "interpolate") {
    // ["interpolate", type, input, stop, output, stop, output, ...]
    scaleAt(value.map((_, i) => i).filter((i) => i >= 4 && i % 2 === 0));
  } else if (op === "step") {
    // ["step", input, output, stop, output, ...]
    scaleAt(value.map((_, i) => i).filter((i) => i === 2 || (i > 2 && i % 2 === 0)));
  } else if (op === "match") {
    // ["match", input, label, output, ..., default]
    scaleAt(value.map((_, i) => i).filter((i) => i >= 3 && (i % 2 === 1 || i === value.length - 1)));
  } else if (op === "case") {
    // ["case", condition, output, ..., default]
    scaleAt(value.map((_, i) => i).filter((i) => (i >= 2 && i % 2 === 0) || i === value.length - 1));
  } else {
    throw new Error(`cannot scale size expression "${op}"`);
  }
  return scaled;
}

const manifest = read("layers/explore/manifest.json");
const layers = [];
const unresolvedTokens = new Set();
const substitutedFonts = new Set();
const scaledLabels = [];
const overriddenLayers = [];
const extraLayers = [];
let skipped = 0;

for (const path of manifest) {
  const spec = read(path);
  if (EXCLUDED_SOURCES.has(spec.source)) {
    skipped++;
    continue;
  }
  const id = path.replace(/^layers\/explore\//, "").replace(/\.json$/, "").replace(/\//g, "-");
  const { layer, unresolved } = resolveLayerSpec({ ...spec, id });
  unresolved.forEach((t) => unresolvedTokens.add(t));
  if (layer.layout?.["text-font"]) {
    layer.layout["text-font"] = substituteFonts(layer.layout["text-font"], substitutedFonts);
  }
  if (LABEL_SIZE_SCALE[id] && layer.layout?.["text-size"] !== undefined) {
    layer.layout["text-size"] = scaleSize(layer.layout["text-size"], LABEL_SIZE_SCALE[id]);
    scaledLabels.push(`${id} x${LABEL_SIZE_SCALE[id]}`);
  }
  // Editor-only bookkeeping upstream; nothing here reads it.
  delete layer.metadata;
  if (LAYER_OVERRIDES[id]) {
    layers.push(merge(layer, LAYER_OVERRIDES[id]));
    overriddenLayers.push(id);
  } else {
    layers.push(layer);
  }
  if (EXTRA_LAYERS[id]) {
    layers.push(...EXTRA_LAYERS[id].map(({ copyOf, ...extra }) => {
      if (!copyOf) return extra;
      const source = layers.find((l) => l.id === copyOf);
      if (!source) throw new Error(`${extra.id}: copyOf layer not found: ${copyOf}`);
      return { ...structuredClone(source), ...extra };
    }));
    extraLayers.push(...EXTRA_LAYERS[id].map((l) => l.id));
  }
}
const missingAnchors = Object.keys(EXTRA_LAYERS).filter((id) => !layers.some((l) => l.id === id));
if (missingAnchors.length) throw new Error(`extra layer anchors not found: ${missingAnchors.join(", ")}`);

const style = {
  version: 8,
  name: "Overture Maps (Positron-style)",
  metadata: {
    "mbtileserver:generated-by": "tools/build-overture-style.mjs",
    "mbtileserver:source": `https://github.com/${REPO}/tree/${COMMIT}`,
    "mbtileserver:license": "MIT (c) 2024 Overture Maps Foundation and affiliates",
  },
  glyphs: GLYPHS,
  // Sources are attached at runtime in map.html, from PMTiles URLs resolved
  // against the Overture STAC catalog.
  sources: {},
  layers,
};

writeFileSync(outFile, `${JSON.stringify(style, null, 2)}\n`);
rmSync(tmpDir, { recursive: true, force: true });

console.log(`wrote ${outFile}`);
console.log(`  ${layers.length} layers (${skipped} skipped from: ${[...EXCLUDED_SOURCES].join(", ")})`);
console.log(`  sources referenced: ${[...new Set(layers.map((l) => l.source).filter(Boolean))].sort().join(", ")}`);
if (substitutedFonts.size) console.log(`  font substitutions: ${[...substitutedFonts].join(", ")}`);
if (scaledLabels.length) console.log(`  label sizes scaled: ${scaledLabels.join(", ")}`);
console.log(`  palette: ${Object.keys(PALETTE).length ? `${Object.keys(PALETTE).length} color tokens replaced (CARTO Positron-style)` : "Overture default"}`);
if (overriddenLayers.length) console.log(`  layer overrides: ${overriddenLayers.join(", ")}`);
if (extraLayers.length) console.log(`  extra layers: ${extraLayers.join(", ")}`);
if (unresolvedTokens.size) {
  console.error(`  UNRESOLVED TOKENS: ${[...unresolvedTokens].join(", ")}`);
  process.exit(1);
}
