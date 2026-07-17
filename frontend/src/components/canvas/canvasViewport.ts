// React Flow defaults to 0.5, which prevents a large topology from fitting in one viewport.
// A tenth of one percent lets Fit View frame even the supported 2,000-node upper bound; ordinary
// graphs still resolve to their naturally higher readable zoom.
export const MIN_CANVAS_ZOOM = 0.001;
