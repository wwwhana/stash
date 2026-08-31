(function (root, factory) {
    const api = factory();
    if (typeof module === 'object' && module.exports) module.exports = api;
    else root.StashGraphViewportViewModel = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    'use strict';

    const MIN_SCALE = 0.35;
    const MAX_SCALE = 1.6;
    const DEFAULT_SCALE = 1;
    const ZOOM_FACTOR = 1.15;
    const FIT_PADDING = 24;
    const KEYBOARD_PAN_STEP = 48;

    function finite(value, fallback = 0) {
        const number = Number(value);
        return Number.isFinite(number) ? number : fallback;
    }

    function positive(value, fallback = 0) {
        const number = finite(value, fallback);
        return number > 0 ? number : fallback;
    }

    function clampScale(value) {
        return Math.max(MIN_SCALE, Math.min(MAX_SCALE, finite(value, DEFAULT_SCALE)));
    }

    function effectiveScale(value) {
        return Math.max(Number.EPSILON, Math.min(MAX_SCALE, finite(value, DEFAULT_SCALE)));
    }

    function emptyViewport() {
        return {
            scale: DEFAULT_SCALE,
            panX: 0,
            panY: 0,
            viewportWidth: 0,
            viewportHeight: 0,
            worldWidth: 0,
            worldHeight: 0
        };
    }

    function eventElement(value) {
        if (!value || typeof value !== 'object') return null;
        if (value.currentTarget && typeof value.currentTarget === 'object') return value.currentTarget;
        return value;
    }

    function elementSize(value) {
        const element = eventElement(value);
        const rect = element && typeof element.getBoundingClientRect === 'function'
            ? element.getBoundingClientRect()
            : null;
        return {
            width: positive(element && (element.clientWidth || element.offsetWidth || element.width), positive(rect && rect.width)),
            height: positive(element && (element.clientHeight || element.offsetHeight || element.height), positive(rect && rect.height)),
            left: finite(rect && rect.left, finite(element && element.left)),
            top: finite(rect && rect.top, finite(element && element.top))
        };
    }

    function graphSize(value) {
        const layout = value && typeof value === 'object' ? value : {};
        return {
            width: positive(layout.width || layout.worldWidth),
            height: positive(layout.height || layout.worldHeight)
        };
    }

    function nodeCenter(value) {
        const node = value && value.item && value.x === undefined ? value.item : value;
        if (!node || typeof node !== 'object') return null;
        const x = finite(node.x, finite(node.centerX, NaN));
        const y = finite(node.y, finite(node.centerY, NaN));
        if (Number.isFinite(x) && Number.isFinite(y)) return { x, y };
        const left = finite(node.left, NaN);
        const top = finite(node.top, NaN);
        const width = positive(node.width);
        const height = positive(node.height);
        if (Number.isFinite(left) && Number.isFinite(top)) {
            return { x: left + width / 2, y: top + height / 2 };
        }
        return null;
    }

    function createGraphViewportViewModel() {
        return {
            graphViewport: emptyViewport(),
            graphViewportPanState: null,
            graphViewportDragging: false,
            graphViewportAnnouncement: '',

            graphViewportLimits() {
                return { min: MIN_SCALE, max: MAX_SCALE };
            },

            graphViewportScale() {
                return effectiveScale(this.graphViewport && this.graphViewport.scale);
            },

            graphViewportZoomPercent() {
                return Math.round(this.graphViewportScale() * 100) + '%';
            },

            graphViewportWorldSize(layout = null) {
                const source = layout || this.workGraphLayout || this.graphLayout || this.graphViewport;
                const size = graphSize(source);
                if (size.width || size.height) {
                    return size;
                }
                return {
                    width: positive(this.graphViewport && this.graphViewport.worldWidth),
                    height: positive(this.graphViewport && this.graphViewport.worldHeight)
                };
            },

            graphViewportMeasure(viewport = null) {
                const size = elementSize(viewport);
                const current = this.graphViewport || emptyViewport();
                this.graphViewport = {
                    ...current,
                    viewportWidth: size.width,
                    viewportHeight: size.height
                };
                return this.graphViewport;
            },

            resizeGraphViewport(viewport = null) {
                const size = elementSize(viewport);
                const current = this.graphViewport || emptyViewport();
                if (!size.width || !size.height) return current;
                const previousWidth = positive(current.viewportWidth);
                const previousHeight = positive(current.viewportHeight);
                this.graphViewport = {
                    ...current,
                    panX: finite(current.panX) + (previousWidth ? (size.width - previousWidth) / 2 : 0),
                    panY: finite(current.panY) + (previousHeight ? (size.height - previousHeight) / 2 : 0),
                    viewportWidth: size.width,
                    viewportHeight: size.height
                };
                return this.graphViewport;
            },

            setGraphViewportLayout(layout, viewport = null) {
                const world = this.graphViewportWorldSize(layout);
                const current = this.graphViewport || emptyViewport();
                const measured = viewport ? elementSize(viewport) : {};
                this.graphViewport = {
                    ...current,
                    worldWidth: world.width,
                    worldHeight: world.height,
                    viewportWidth: measured.width || current.viewportWidth,
                    viewportHeight: measured.height || current.viewportHeight
                };
                return this.graphViewport;
            },

            updateGraphViewport(layout, viewport = null) {
                return this.setGraphViewportLayout(layout, viewport);
            },

            resetGraphViewport(viewport = null) {
                const current = this.graphViewport || emptyViewport();
                const size = viewport ? elementSize(viewport) : {};
                this.graphViewport = {
                    ...current,
                    scale: DEFAULT_SCALE,
                    panX: 0,
                    panY: 0,
                    viewportWidth: size.width || current.viewportWidth,
                    viewportHeight: size.height || current.viewportHeight
                };
                return this.graphViewport;
            },

            setGraphViewportScale(value, anchor = null) {
                const current = this.graphViewport || emptyViewport();
                const nextScale = clampScale(value);
                if (!anchor || !Number.isFinite(Number(anchor.x)) || !Number.isFinite(Number(anchor.y))) {
                    this.graphViewport = { ...current, scale: nextScale };
                    return this.graphViewport;
                }
                const oldScale = effectiveScale(current.scale);
                const worldX = (finite(anchor.x) - finite(current.panX)) / oldScale;
                const worldY = (finite(anchor.y) - finite(current.panY)) / oldScale;
                this.graphViewport = {
                    ...current,
                    scale: nextScale,
                    panX: finite(anchor.x) - worldX * nextScale,
                    panY: finite(anchor.y) - worldY * nextScale
                };
                return this.graphViewport;
            },

            zoomGraphViewport(direction, anchor = null) {
                const sign = Number(direction) < 0 ? -1 : 1;
                const current = this.graphViewport || emptyViewport();
                const factor = sign > 0 ? ZOOM_FACTOR : 1 / ZOOM_FACTOR;
                return this.setGraphViewportScale(clampScale(current.scale) * factor, anchor);
            },

            zoomGraphIn(anchor = null) {
                return this.zoomGraphViewport(1, anchor);
            },

            zoomGraphOut(anchor = null) {
                return this.zoomGraphViewport(-1, anchor);
            },

            zoomGraphViewportAtCenter(direction, viewport = null) {
                const current = this.graphViewport || emptyViewport();
                const size = elementSize(viewport || current);
                return this.zoomGraphViewport(direction, {
                    x: (size.width || finite(current.viewportWidth)) / 2,
                    y: (size.height || finite(current.viewportHeight)) / 2
                });
            },

            graphViewportWheel(event, viewport = null) {
                if (!event) return this.graphViewport;
                if (event.ctrlKey || event.metaKey) return this.graphViewport;
                const target = viewport || event.currentTarget;
                const size = elementSize(target);
                const current = this.graphViewport || emptyViewport();
                const deltaUnit = Number(event.deltaMode) === 1
                    ? 16
                    : (Number(event.deltaMode) === 2 ? Math.max(1, size.height || current.viewportHeight) : 1);
                let deltaX = finite(event.deltaX) * deltaUnit;
                let deltaY = finite(event.deltaY) * deltaUnit;
                if (event.shiftKey && !deltaX) {
                    deltaX = deltaY;
                    deltaY = 0;
                }
                if (!deltaX && !deltaY) return this.graphViewport;
                if (event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                this.graphViewport = {
                    ...current,
                    viewportWidth: size.width || current.viewportWidth,
                    viewportHeight: size.height || current.viewportHeight
                };
                if (event.altKey) {
                    const localX = finite(event.clientX) - size.left;
                    const localY = finite(event.clientY) - size.top;
                    const zoomDelta = deltaY || deltaX;
                    const direction = zoomDelta < 0 ? 1 : -1;
                    const result = this.zoomGraphViewport(direction, { x: localX, y: localY });
                    this.graphViewportAnnouncement = `${direction > 0 ? '확대' : '축소'} ${this.graphViewportZoomPercent()}`;
                    return result;
                }
                this.graphViewport = {
                    ...this.graphViewport,
                    panX: finite(this.graphViewport.panX) - deltaX,
                    panY: finite(this.graphViewport.panY) - deltaY
                };
                this.graphViewportAnnouncement = '작업 흐름 위치를 옮겼습니다.';
                return this.graphViewport;
            },

            zoomGraphAtPointer(event, viewport = null) {
                return this.graphViewportWheel(event, viewport);
            },

            graphViewportKeydown(event, viewport = null, layout = null) {
                if (!event || event.altKey || event.ctrlKey || event.metaKey) return false;
                const target = viewport || event.currentTarget;
                const size = elementSize(target);
                const current = this.graphViewport || emptyViewport();
                const key = String(event.key || '');
                const step = event.shiftKey ? KEYBOARD_PAN_STEP * 2 : KEYBOARD_PAN_STEP;
                const pan = {
                    ArrowLeft: [step, 0],
                    ArrowRight: [-step, 0],
                    ArrowUp: [0, step],
                    ArrowDown: [0, -step]
                }[key];
                if (pan) {
                    this.graphViewport = {
                        ...current,
                        panX: finite(current.panX) + pan[0],
                        panY: finite(current.panY) + pan[1],
                        viewportWidth: size.width || current.viewportWidth,
                        viewportHeight: size.height || current.viewportHeight
                    };
                    this.graphViewportAnnouncement = '작업 흐름 위치를 옮겼습니다.';
                } else if (key === '+' || key === '=' || key === 'Add') {
                    this.zoomGraphIn({
                        x: (size.width || finite(current.viewportWidth)) / 2,
                        y: (size.height || finite(current.viewportHeight)) / 2
                    });
                    this.graphViewportAnnouncement = `확대 ${this.graphViewportZoomPercent()}`;
                } else if (key === '-' || key === '_' || key === 'Subtract') {
                    this.zoomGraphOut({
                        x: (size.width || finite(current.viewportWidth)) / 2,
                        y: (size.height || finite(current.viewportHeight)) / 2
                    });
                    this.graphViewportAnnouncement = `축소 ${this.graphViewportZoomPercent()}`;
                } else if (key === 'Home') {
                    this.graphViewportFit(target, layout);
                    this.graphViewportAnnouncement = `전체 보기 ${this.graphViewportZoomPercent()}`;
                } else if (key === 'End' && this.graphFocusedKey) {
                    this.centerGraphNodeByID(this.graphFocusedKey, target);
                    this.graphViewportAnnouncement = '선택한 작업을 가운데로 옮겼습니다.';
                } else {
                    return false;
                }
                if (event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                return true;
            },

            graphViewportFit(viewport = null, layout = null) {
                const current = this.graphViewport || emptyViewport();
                const target = viewport || current;
                const size = elementSize(target);
                const world = this.graphViewportWorldSize(layout);
                const viewportWidth = size.width || finite(current.viewportWidth);
                const viewportHeight = size.height || finite(current.viewportHeight);
                if (!viewportWidth || !viewportHeight) {
                    this.graphViewport = {
                        ...current,
                        worldWidth: world.width,
                        worldHeight: world.height
                    };
                    return this.graphViewport;
                }
                const availableWidth = Math.max(1, viewportWidth - FIT_PADDING * 2);
                const availableHeight = Math.max(1, viewportHeight - FIT_PADDING * 2);
                const fitted = world.width && world.height
                    ? Math.min(availableWidth / world.width, availableHeight / world.height)
                    : DEFAULT_SCALE;
                const scale = effectiveScale(fitted);
                const panX = (viewportWidth - world.width * scale) / 2;
                const panY = (viewportHeight - world.height * scale) / 2;
                this.graphViewport = {
                    ...current,
                    scale,
                    panX: Number.isFinite(panX) ? panX : 0,
                    panY: Number.isFinite(panY) ? panY : 0,
                    viewportWidth,
                    viewportHeight,
                    worldWidth: world.width,
                    worldHeight: world.height
                };
                return this.graphViewport;
            },

            scheduleGraphViewportFit(viewport = null, layout = null, attempts = 4) {
                const target = viewport;
                const run = remaining => {
                    const size = elementSize(target);
                    if (size.width && size.height) {
                        this.setGraphViewportLayout(layout, target);
                        this.graphViewportFit(target, layout);
                        return true;
                    }
                    if (remaining <= 0) return false;
                    const schedule = typeof requestAnimationFrame === 'function'
                        ? requestAnimationFrame
                        : callback => setTimeout(callback, 0);
                    schedule(() => run(remaining - 1));
                    return false;
                };
                return run(Math.max(0, Number(attempts) || 0));
            },

            fitGraphViewport(viewport = null, layout = null) {
                return this.graphViewportFit(viewport, layout);
            },

            graphViewportIsEmptyTarget(event) {
                const target = event && event.target;
                if (!target || typeof target.closest !== 'function') return true;
                return !target.closest('[data-graph-node-key], button, a, input, select, textarea');
            },

            startGraphViewportPan(event, viewport = null) {
                if (!event || (event.isPrimary === false) || (event.button !== undefined && event.button !== 0)) return false;
                if (!this.graphViewportIsEmptyTarget(event)) return false;
                const current = this.graphViewport || emptyViewport();
                const target = viewport || event.currentTarget;
                const handle = event.currentTarget || target;
                this.graphViewportPanState = {
                    pointerId: event.pointerId,
                    startX: finite(event.clientX),
                    startY: finite(event.clientY),
                    originX: finite(current.panX),
                    originY: finite(current.panY),
                    viewport: target
                };
                this.graphViewportDragging = true;
                if (event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                if (handle && typeof handle.setPointerCapture === 'function' && event.pointerId !== undefined) {
                    try { handle.setPointerCapture(event.pointerId); } catch (_) {}
                }
                return true;
            },

            moveGraphViewportPan(event) {
                const state = this.graphViewportPanState;
                if (!state || !event || (state.pointerId !== undefined && event.pointerId !== state.pointerId)) return false;
                if (event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                const current = this.graphViewport || emptyViewport();
                this.graphViewport = {
                    ...current,
                    panX: state.originX + finite(event.clientX) - state.startX,
                    panY: state.originY + finite(event.clientY) - state.startY
                };
                return true;
            },

            finishGraphViewportPan(event = null) {
                const state = this.graphViewportPanState;
                if (!state || (event && state.pointerId !== undefined && event.pointerId !== state.pointerId)) return false;
                const handle = state.viewport;
                if (handle && typeof handle.releasePointerCapture === 'function' && state.pointerId !== undefined) {
                    try { handle.releasePointerCapture(state.pointerId); } catch (_) {}
                }
                this.graphViewportPanState = null;
                this.graphViewportDragging = false;
                return true;
            },

            cancelGraphViewportPan(event = null) {
                const state = this.graphViewportPanState;
                if (!state) return false;
                if (event && event.cancelable && typeof event.preventDefault === 'function') event.preventDefault();
                const current = this.graphViewport || emptyViewport();
                this.graphViewport = {
                    ...current,
                    panX: state.originX,
                    panY: state.originY
                };
                this.graphViewportPanState = null;
                this.graphViewportDragging = false;
                return true;
            },

            centerGraphViewportNode(node, viewport = null) {
                const center = nodeCenter(node);
                if (!center) return this.graphViewport;
                const current = this.graphViewport || emptyViewport();
                const size = elementSize(viewport || current);
                const width = size.width || finite(current.viewportWidth);
                const height = size.height || finite(current.viewportHeight);
                const scale = effectiveScale(current.scale);
                this.graphViewport = {
                    ...current,
                    panX: width / 2 - center.x * scale,
                    panY: height / 2 - center.y * scale,
                    viewportWidth: width,
                    viewportHeight: height
                };
                return this.graphViewport;
            },

            centerGraphNode(node, viewport = null) {
                return this.centerGraphViewportNode(node, viewport);
            },

            centerGraphNodeByID(id, viewport = null) {
                const nodes = this.workGraphLayout && Array.isArray(this.workGraphLayout.nodes)
                    ? this.workGraphLayout.nodes
                    : [];
                const node = nodes.find(candidate => (
                    String(candidate && candidate.key) === String(id) ||
                    String(candidate && candidate.id) === String(id) ||
                    String(candidate && candidate.item && candidate.item.id) === String(id)
                ));
                return node ? this.centerGraphViewportNode(node, viewport) : this.graphViewport;
            },

            graphViewportWorldStyle() {
                const current = this.graphViewport || emptyViewport();
                const world = this.graphViewportWorldSize();
                return `width:${world.width}px;height:${world.height}px;transform:translate(${finite(current.panX)}px,${finite(current.panY)}px) scale(${effectiveScale(current.scale)});transform-origin:0 0;`;
            },

            worldStyle() {
                return this.graphViewportWorldStyle();
            },

            graphViewportSpaceStyle(viewport = null) {
                const current = this.graphViewport || emptyViewport();
                const world = this.graphViewportWorldSize();
                const size = elementSize(viewport || current);
                const scaledWidth = world.width * effectiveScale(current.scale);
                const scaledHeight = world.height * effectiveScale(current.scale);
                const width = Math.max(size.width || finite(current.viewportWidth), scaledWidth + Math.max(0, finite(current.panX)));
                const height = Math.max(size.height || finite(current.viewportHeight), scaledHeight + Math.max(0, finite(current.panY)));
                return `width:${Math.max(0, width)}px;height:${Math.max(0, height)}px;`;
            },

            spaceStyle(viewport = null) {
                return this.graphViewportSpaceStyle(viewport);
            },

            graphViewportTransformPoint(point) {
                const current = this.graphViewport || emptyViewport();
                const value = point && typeof point === 'object' ? point : {};
                const scale = effectiveScale(current.scale);
                return {
                    x: finite(value.x) * scale + finite(current.panX),
                    y: finite(value.y) * scale + finite(current.panY)
                };
            },

            graphViewportInversePoint(point) {
                const current = this.graphViewport || emptyViewport();
                const value = point && typeof point === 'object' ? point : {};
                const scale = effectiveScale(current.scale);
                return {
                    x: (finite(value.x) - finite(current.panX)) / scale,
                    y: (finite(value.y) - finite(current.panY)) / scale
                };
            }
        };
    }

    return {
        MIN_SCALE,
        MAX_SCALE,
        createGraphViewportViewModel
    };
}));
