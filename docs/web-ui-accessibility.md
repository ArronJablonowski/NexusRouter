# Web UI accessibility and browser support

NexusRouter's embedded chat and Workboard UI targets WCAG 2.2 Level AA. The
application remains usable without pointer input: navigation, board selection,
filters, card disclosure, adjacent reordering, lifecycle controls, acceptance
review, dialogs, refresh, and reconciliation are native keyboard controls.

## Browser matrix

The minimum versions below correspond to the Web UI's required platform APIs,
including `fetch`, `EventSource`, `TextEncoder`, `inert`, `replaceChildren`, and
`focus({preventScroll})`.

| Browser | Minimum | Support level | Release evidence |
| --- | ---: | --- | --- |
| Google Chrome / Chromium | 120 | Qualified only by a recorded no-skip run on the release candidate | Automated authenticated shell, keyboard, accessibility-tree, CRUD, conflict, lifecycle, and focus tests run on the selected Chrome binary. |
| Microsoft Edge | 120 | Compatibility target; not qualified for v1 | A manual current-stable smoke test is required before claiming support. |
| Mozilla Firefox | 121 | Compatibility target; not qualified for v1 | A manual current-stable keyboard, dialog, streaming fallback, and layout smoke test is required before claiming support. |
| Apple Safari | 17.2 | Compatibility target; not qualified for v1 | A manual current-stable macOS keyboard, VoiceOver landmark, dialog, streaming fallback, and layout smoke test is required before claiming support. |
| Mobile browsers | — | Not supported for v1 | The responsive layout is retained, but touch and mobile assistive-technology qualification are deferred. |

Release qualification must record the browser's exact version and operating
system. A Chromium result must not be reported as direct Firefox, Safari, or
Edge evidence, and compatibility alone is not a support claim.

## Automated gates

The Web UI package enforces the following without network-installed test tools:

- unique element IDs, valid ARIA references, labelled visible form controls,
  natural tab order, skip navigation, landmarks, modal semantics, and live
  status regions in the checked-in shell;
- WCAG AA text contrast for every named palette role and 3:1 form-control
  boundaries, visible keyboard focus, and reduced-motion handling;
- Chrome's computed accessibility tree has named interactive controls and the
  expected main, navigation, and Workboard region landmarks;
- real-browser tests exercise Enter activation, canonical list/Kanban views,
  card movement, modal focus traps and restoration, filters, authoritative
  refresh focus, lifecycle state, acceptance review, and conflict recovery.

These gates supplement rather than replace assistive-technology testing.

## Manual release checklist

Run this checklist in every browser whose matrix row requires a manual smoke
test. Use only local fixtures or a local-only daemon during release validation.

1. Complete one-time browser connection and confirm the page exposes one main
   landmark, labelled primary navigation, and correctly ordered headings.
2. With the keyboard only, move between Chats and Workboards, select a board,
   apply and reset filters, switch Kanban/list presentation, disclose a card,
   and activate every enabled lifecycle or position control.
3. Open each mutation and acceptance dialog. Confirm focus enters the dialog,
   Tab and Shift+Tab remain inside it, Escape closes it when no mutation is in
   flight, background controls are unavailable, and focus returns to the opener
   or stable Refresh fallback.
4. Trigger a validation error, stale revision, mutation success, and reconnect
   failure. Confirm the status is announced without moving focus and that no
   provisional state is presented as committed.
5. Test 200% browser zoom at 1280 CSS pixels and a 320 CSS-pixel viewport.
   Content must reflow without loss of controls or two-dimensional page scroll.
6. Enable reduced motion and the platform's increased-contrast/forced-colors
   mode. Confirm focus remains visible and state is not communicated by color
   alone.
7. In Safari, use VoiceOver to verify landmarks, named controls, card state,
   evidence source, advisory-model wording, and dialog announcements.
8. Record browser version, OS, result, defects, and tester in the release
   qualification evidence. Any failed required row blocks release.
