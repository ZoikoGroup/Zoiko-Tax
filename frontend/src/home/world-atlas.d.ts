// The world map's geometry: Natural Earth 1:110m country boundaries (public
// domain), as TopoJSON from world-atlas. Declared here so the compiler takes
// the shape from the TopoJSON specification instead of inferring a type from
// a 100 KB document.
declare module 'world-atlas/countries-110m.json' {
  import type { GeometryCollection, Topology } from 'topojson-specification'

  const world: Topology<{ countries: GeometryCollection<{ name: string }> }>
  export default world
}
