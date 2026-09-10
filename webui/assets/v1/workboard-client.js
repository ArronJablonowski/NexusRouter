"use strict";
(function (root, factory) {
	const client = factory();
	if (typeof module === "object" && module.exports) module.exports = client;
	if (root) root.DarwinWorkboardClient = client;
})(typeof window === "object" ? window : null, () => {
	function compareText(left, right) {
		const a = new TextEncoder().encode(left), b = new TextEncoder().encode(right), count = Math.min(a.length, b.length);
		for (let index = 0; index < count; index++) if (a[index] !== b[index]) return a[index] - b[index];
		return a.length - b.length;
	}
	function current(requestGeneration, activeGeneration, requestedBoard, selectedBoard) {
		return requestGeneration === activeGeneration && (requestedBoard === undefined || requestedBoard === selectedBoard);
	}
	function canonical(cards, previous, existingIDs, states) {
		const ids = new Set(existingIDs), ranks = new Map(), stateIndex = new Map(states.map((state, index) => [state, index]));
		let prior = previous || null;
		for (const card of cards) {
			if (!card || ids.has(card.id) || !stateIndex.has(card.state)) return null;
			if (prior) {
				const stateOrder = stateIndex.get(card.state) - stateIndex.get(prior.state), rankOrder = compareText(card.rank, prior.rank);
				if (stateOrder < 0 || stateOrder === 0 && (rankOrder < 0 || rankOrder === 0 && compareText(card.id, prior.id) <= 0)) return null;
			}
			ids.add(card.id); ranks.set(card.state, card.rank); prior = card;
		}
		return Object.freeze({last: prior, ranks});
	}
	function reparent(cards, nodes, targetFor, focused) {
		for (const card of cards) { const node = nodes.get(card.id), target = targetFor(card); if (!node || !target) return false; target.append(node); }
		if (focused && cards.some(card => nodes.get(card.id).contains(focused))) focused.focus({preventScroll: true});
		return true;
	}
	return Object.freeze({canonical, compareText, current, reparent});
});
