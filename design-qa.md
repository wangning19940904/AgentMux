# Framework authentication design QA

- Normalized source: [`artifacts/framework-auth/source.png`](artifacts/framework-auth/source.png)
- Final implementation: [`artifacts/framework-auth/final.jpg`](artifacts/framework-auth/final.jpg)
- Focused source table: [`artifacts/framework-auth/source-table.png`](artifacts/framework-auth/source-table.png)
- Focused final table: [`artifacts/framework-auth/final-table.jpg`](artifacts/framework-auth/final-table.jpg)
- Interaction-state capture: [`artifacts/framework-auth/success.jpg`](artifacts/framework-auth/success.jpg)
- Viewport: 1100 × 752 desktop Wails window
- Density normalization: source was 2200 × 1504 at 2× and was downsampled to 1100 × 752; implementation was captured at 1100 × 752. Focused table crops are both 795 × 343.
- Compared state: framework catalogue with installed Claude Code and Codex rows. The source targets `aliyun-swas-sg`; the final visual capture uses the local machine because it has the updated authentication lifecycle API and deterministic installed-framework state.

## Full-view comparison evidence

The final screen preserves the source shell, sidebar, topbar, typography, restrained teal palette, table structure, row density, badges, radii, and icon language. The existing current-product prerequisite card remains above the catalogue; this predates the authentication change and is an intentional product constraint rather than design drift introduced by this work.

The authentication addition is visually contained within the existing catalogue: a summary pill in the section header, `网页登录` in the requirements column, an authentication badge in the status stack, and one primary plus one secondary action in the existing action column.

## Focused comparison evidence

Focused table crops confirm that:

- framework names, CLI labels, environment-key pills, version badges, and row separators keep the source hierarchy;
- `已登录` uses the existing success token rather than introducing a new color;
- `登录`/`重新登录` and maintenance actions remain fully visible at the default desktop width;
- the new authentication content wraps within the original row rhythm without clipping or forcing horizontal scrolling.

## Required fidelity surfaces

- Fonts and typography: passed. Existing system font stack, weights, monospace tokens, badge sizes, and table hierarchy are preserved.
- Spacing and layout rhythm: passed. Catalogue padding, row separators, 8px control radii, and action spacing match the established component system.
- Colors and visual tokens: passed. Authentication uses existing accent, success, warning, muted, border, and elevated-surface variables.
- Image quality and asset fidelity: passed. The supplied AgentMux logo and Lucide icon system are preserved; no raster placeholders, handcrafted SVGs, or substitute artwork were introduced.
- Copy and content: passed. Labels distinguish installation, browser login, environment credentials, authenticated state, and maintenance actions. Machine-specific copy names where the login takes effect.

## Interaction verification

- Switched the target selector from `aliyun-swas-sg` to the local machine and confirmed the page reloaded the correct machine-scoped auth states.
- Confirmed initial `正在检查登录` states resolve to `已登录`, `需要登录`, or credential setup states.
- Exercised the row-level `重新登录` action and captured the contextual success row.
- Automated tests cover browser/device URL extraction, verification codes, Claude code submission, completed sessions, cancellation, missing sessions, and authentication polling.
- No visible runtime error appeared in the Wails UI. WebView developer-console output is not exposed in the packaged desktop build; TypeScript production build and all browser-independent frontend tests passed.

## Comparison history

### Iteration 1

- Finding: P1 — the general catalogue minimum width left the right-side authentication and maintenance actions partly outside the visible default Wails window.
- Fix: reduced only `.framework-table` to a 760px minimum and rebalanced its five fixed column widths to 27% / 8% / 22% / 25% / 18%.
- Post-fix evidence: `artifacts/framework-auth/final.jpg` and `artifacts/framework-auth/final-table.jpg` show complete button labels and borders inside the viewport.

### Iteration 2

- Finding: P1 — re-authentication could be declared successful from the pre-existing login state before the new CLI login process completed.
- Fix: added server-owned `starting / waiting / succeeded / failed / cancelled` lifecycle snapshots and made re-authentication wait for the current process result.
- Post-fix evidence: lifecycle regression tests pass for successful completion and cancellation; the final catalogue remains visually unchanged.

## Remaining P3 follow-up

- A separate visual pass at the sub-820px card layout would be useful if compact desktop windows become a primary use case. Responsive CSS is present, but this handoff was visually verified at the supplied desktop target size.

final result: passed
