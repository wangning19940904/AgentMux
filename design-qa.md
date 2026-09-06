**Comparison Target**

- Source visual truth: `/tmp/multica-native-agents-reference.png`
- Implementation screenshot: `/tmp/agentmux-two-level-final.png`
- Full-view comparison: `/tmp/multica-agentmux-two-level-comparison.png`
- Focused top/sidebar comparison: `/tmp/multica-agentmux-two-level-detail.png`
- Viewport: source 1229 × 768 px; implementation 1228 × 768 CSS px at device scale 1.
- Normalization: implementation resized by one horizontal pixel for the combined comparison; no density conversion.
- State: light theme, Agents route, sidebar expanded, no dialog. Multica has populated data while AgentMux's local registry is empty, so row-content differences are excluded from layout scoring.
- Intentional structural deviation: the user explicitly requested two navigation levels. AgentMux therefore splits Multica's 247 px sidebar footprint into a 64 px group rail and a 183 px section rail while retaining the reference's main-canvas geometry.

**Findings**

- No actionable P0, P1, or P2 findings remain.
- Fonts and typography: native macOS system stack, small UI optical weights, muted secondary copy, truncation, and active-label contrast remain consistent with the reference.
- Spacing and layout rhythm: the combined navigation width is 247 px; tab strip, canvas start, 12 px radius, toolbar heights, gutters, and content alignment track the Multica reference. The intentional rail split is clearly separated by a hairline border.
- Colors and visual tokens: both navigation levels use the same Multica-derived shell gray, neutral active fill, white canvas, hairline borders, and black primary action.
- Image and icon fidelity: the existing AgentMux logo and Lucide icon library are used. No source raster assets were replaced by CSS drawings, placeholder art, or text glyphs.
- Copy and content: product-specific AgentMux labels and data are intentionally retained. The first level names product domains; the second level names actionable destinations.

**Open Questions**

- None blocking.

**Comparison History**

1. Previous approved single-level state — `/tmp/agentmux-final-reference-state.png`
   - The Multica frame, toolbar, and content tokens matched, but the user requested the information architecture be changed to two levels.
2. First two-level implementation — `/tmp/agentmux-two-level-v1.png`
   - [P2] Needed confirmation that switching a first-level group updates the second-level destinations and retains a stable 247 px total width.
   - Fix/verification: first-level destinations were wired through `primaryGroupDestination`; active group and second-level contents derive from the current route; persistent widths were versioned to 64 + 183 px.
3. Final two-level comparison — `/tmp/multica-agentmux-two-level-comparison.png` and `/tmp/multica-agentmux-two-level-detail.png`
   - Post-fix evidence confirms stable main-canvas alignment, clear two-level hierarchy, consistent Multica visual tokens, and no actionable P0/P1/P2 mismatch.

**Interaction and Responsive Checks**

- First-level group navigation: Agents → Connectivity passed.
- Second-level navigation: Connectivity → Schedules passed.
- Cross-group search: searching for “用量” navigated directly to Usage and updated both navigation levels.
- “New Agent” in the second level opened the real Agent creation drawer.
- Mobile viewport 390 × 844: two desktop sidebars collapse to the existing bottom navigation; content and primary action remain visible.
- Browser console: no application errors; only Vite connection and React DevTools development messages.

**Follow-up Polish**

- [P3] The user-requested icon rail is an intentional departure from Multica's single sidebar, but its combined footprint and visual styling preserve the reference composition.

**Implementation Checklist**

- [x] Add a persistent 64 px first-level group rail.
- [x] Add a persistent/resizable 183 px second-level destination rail.
- [x] Keep the combined navigation width aligned to Multica's 247 px source.
- [x] Preserve tab strip, inset canvas, collection toolbar, typography, colors, and radii.
- [x] Verify group switching, destination navigation, search, create drawer, and mobile fallback.

final result: passed

## Navigation regression follow-up — 2026-09-04

The visual checks above did not cover a cold lazy-panel load or the full search
popup hit area. Subsequent review found and fixed both gaps:

- New Agent now uses an acknowledged React request instead of a timed window event.
  A delayed-module test covers first open, StrictMode, closing, remounting, and reopening.
- Search results render in a body portal anchored to the input, outside the clipped
  secondary sidebar. Tests cover viewport/sidebar resizing, scrolling, result focus
  and clicks, outside dismissal, Escape, and a hidden mobile anchor.
- Playwright against the local app verified cold-load creation, repeat creation,
  search navigation and Escape at 1228 × 768, plus mobile fallback at 390 × 844.
  The 260 px popup extends beyond the sidebar and remains hit-testable. No page or
  console errors were recorded. Browser plugin was unavailable; bundled Playwright
  used the existing Chromium executable. Agent saving and provider execution were
  not exercised; these checks did not create or modify backend data.
