# Flyke brand assets

The bird and wordmark are redrawn as vectors, so every SVG here stays sharp at any size. The text is converted to outlines, so no font needs to be installed to display them.

<p align="center"><img src="logo/flyke-logo.svg" width="360" alt="Flyke"></p>

## Which file to use

| Where | File |
| --- | --- |
| README, docs, blog posts (light background) | `logo/flyke-logo.svg` |
| Dark backgrounds, dark-mode docs | `logo/flyke-logo-white-text.svg` |
| One-colour print, stamps, engraving | `logo/flyke-logo-black.svg`, `logo/flyke-logo-white.svg` |
| Square spaces (sidebars, title slides) | `logo/flyke-logo-stacked.svg`, `logo/flyke-logo-stacked-white-text.svg` |
| The bird alone | `mark/flyke-mark.svg` (tight box), `mark/flyke-mark-square.svg`, `mark/flyke-mark-black.svg`, `mark/flyke-mark-white.svg` |
| Text only | `wordmark/flyke-wordmark.svg`, `wordmark/flyke-wordmark-white.svg` |
| Browser tab | `icon/favicon.svg` + `icon/favicon.ico` (16, 32, 48 px) |
| iOS home screen | `png/apple-touch-icon.png` (180 px, square; iOS rounds the corners) |
| PWA / Android | `png/icon-192.png`, `png/icon-512.png`, `png/icon-maskable-512.png` |
| GitHub org, Slack, Discord, X avatars | `icon/flyke-app-icon.svg` or `png/icon-512.png` |
| Link previews (Open Graph, X, LinkedIn) | `png/flyke-og-dark.png` or `png/flyke-og-light.png` (1200×630) |
| GitHub repo "Social preview" | `png/flyke-github-social.png` (1280×640) |
| Blog post header image | `png/flyke-blog-header-dark.png` or `png/flyke-blog-header-light.png` (1600×640) |
| Places that reject SVG | `png/flyke-logo.png`, `png/flyke-logo@2x.png`, `png/flyke-mark-512.png`, `png/flyke-mark-1024.png` |

## Snippets

HTML `<head>`:

```html
<link rel="icon" href="/favicon.ico" sizes="48x48">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<meta property="og:image" content="https://<your-site>/flyke-og-dark.png">
<meta name="twitter:card" content="summary_large_image">
```

README or blog post, switching with the reader's GitHub theme:

```html
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="branding/logo/flyke-logo-white-text.svg">
  <img src="branding/logo/flyke-logo.svg" alt="Flyke" width="320">
</picture>
```

## Colours

| Name | Hex | Use |
| --- | --- | --- |
| Sky | `#108cff` | gradient start (wing tips) |
| Indigo | `#4a2cf5` | main body colour |
| Violet | `#5c3aff` | head |
| Magenta | `#a020ff` | gradient end (lower wing) |
| Ink | `#0f172a` | wordmark on light backgrounds |
| Night | `#0b1020` | dark backgrounds |

Brand gradient: `linear-gradient(135deg, #108cff 0%, #4a2cf5 55%, #8a24ff 100%)`.

Wordmark typeface: Plus Jakarta Sans Bold, with 3.5% letter spacing.

## Clear space and minimum size

- Keep empty space around the logo at least as wide as the bird's head.
- Use the full logo at 100 px wide or more. Below that, use the bird alone. Below 24 px, use `favicon.svg` or the app icon.
- Do not recolour the gradient, stretch it, add shadows or outlines, or place the colour logo on busy photos. Use the white version there.

## Regenerating

Everything is generated from `source/build.py`; the bird's paths are defined there. To change something, edit that file and run:

```bash
npm install @resvg/resvg-js@2.6.2 @fontsource/plus-jakarta-sans
uv run --with fonttools python source/build.py node_modules/@fontsource/plus-jakarta-sans/files .
node source/render.js logo/flyke-logo.svg png/flyke-logo.png 1200
```
