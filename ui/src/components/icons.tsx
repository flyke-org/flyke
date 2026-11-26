// Line icons (24px grid, 1.75 stroke), inlined so the embedded dashboard
// needs no icon font or network access.

const PATHS = {
  overview: "M4 4h7v7H4zM13 4h7v4h-7zM13 11h7v9h-7zM4 14h7v6H4z",
  runs: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM10 8.5v7l6-3.5z",
  workflows: "M6 3v12M6 15a3 3 0 1 0 0 6 3 3 0 0 0 0-6zM18 3a3 3 0 1 0 0 6 3 3 0 0 0 0-6zM18 9a9 9 0 0 1-9 9",
  schedules: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  playground: "M8 6l-6 6 6 6M16 6l6 6-6 6M14 4l-4 16",
  events: "M13 2L4 14h7l-1 8 9-12h-7z",
  secrets: "M15 7a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM11.5 12.5L3 21M6 18l2.5 2.5M9.5 14.5L12 17",
  workers: "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01",
  namespaces: "M12 3l9 5-9 5-9-5zM3 12l9 5 9-5M3 16l9 5 9-5",
  search: "M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3",
  sun: "M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4",
  moon: "M20 14.5A8.5 8.5 0 0 1 9.5 4a7 7 0 1 0 10.5 10.5z",
  auto: "M3 5h18v11H3zM8 20h8M12 16v4",
  run: "M5 12h14M13 6l6 6-6 6",
  panel: "M3 4h18v16H3zM9 4v16",
  download: "M12 3v12M7 10l5 5 5-5M4 20h16",
  shield: "M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6zM9 12l2 2 4-4",
  key: "M8 15a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM11 11h10M18 11v3M15 11v2",
  alert: "M12 3l10 17H2zM12 10v4M12 17h.01",
  pause: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM10 9v6M14 9v6",
  failed: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM15 9l-6 6M9 9l6 6",
  check: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM8 12.5l2.5 2.5L16 9.5",
  chevron: "M6 9l6 6 6-6",
} as const;

export type IconName = keyof typeof PATHS;

export function Icon({ name, className }: { name: IconName; className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d={PATHS[name]} />
    </svg>
  );
}

/** The Flyke mark: the white bird on the brand gradient tile. Generated from branding/icon/flyke-app-icon.svg. */
export function Logo({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 1024 1024" role="img" aria-label="Flyke">
      <defs><linearGradient id="logo-bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stopColor="#108cff"/><stop offset=".55" stopColor="#4a2cf5"/><stop offset="1" stopColor="#8a24ff"/></linearGradient>
      <radialGradient id="logo-hl" cx=".25" cy=".1" r=".9"><stop offset="0" stopColor="#fff" stopOpacity=".22"/><stop offset=".6" stopColor="#fff" stopOpacity="0"/></radialGradient></defs>
      <rect width="1024" height="1024" rx="230" fill="url(#logo-bg)"/><rect width="1024" height="1024" rx="230" fill="url(#logo-hl)"/>
      <g transform="translate(132.4 229.5) scale(0.70551)"><defs>
        <clipPath id="logo-neck"><path d="M0 0 H1080 V380 L860 393 C820 442 735 550 676 622 L560 840 H0 Z"/></clipPath>
        <mask id="logo-cut" maskUnits="userSpaceOnUse" x="0" y="0" width="1080" height="840">
          <rect width="1080" height="840" fill="#fff"/>
          <path d="M32 38 C110 78 250 103 454 113 C560 122 660 180 728 270 C770 330 786 400 778 492 C766 432 724 378 650 344 C590 316 500 296 432 289 L265 274 C185 266 130 240 92 192 C62 150 42 98 32 38 Z" fill="#000" stroke="#000" strokeWidth="40" strokeLinejoin="round"/>
          <path d="M173 308 C280 328 380 334 432 337 C520 343 570 350 605 360 C645 370 680 382 702 397 C725 412 745 428 756 441 C768 456 774 470 776 486 C800 500 820 520 810 545 L720 660 L676 624 C688 596 678 560 622 543 C570 527 520 520 464 518 C380 512 330 498 290 470 C248 440 200 382 173 308 Z" fill="#000" stroke="#000" strokeWidth="40" strokeLinejoin="round" clipPath="url(#logo-neck)"/>
        </mask>
      </defs>
      <g fill="#ffffff">
      <path mask="url(#logo-cut)" d="M160 795 L465 515 C600 505 700 490 765 480 L735 330 L748 292 C790 255 840 229 886 229 C930 229 966 250 990 285 C1012 295 1032 310 1042 323 C1045 330 1038 331 1030 330 L985 331 C935 333 893 355 860 393 C820 442 735 550 676 622 C640 660 580 688 508 692 C380 702 260 745 160 795 Z"/>
      <path d="M32 38 C110 78 250 103 454 113 C560 122 660 180 728 270 C770 330 786 400 778 492 C766 432 724 378 650 344 C590 316 500 296 432 289 L265 274 C185 266 130 240 92 192 C62 150 42 98 32 38 Z"/>
      <path clipPath="url(#logo-neck)" d="M173 308 C280 328 380 334 432 337 C520 343 570 350 605 360 C645 370 680 382 702 397 C725 412 745 428 756 441 C768 456 774 470 776 486 C800 500 820 520 810 545 L720 660 L676 624 C688 596 678 560 622 543 C570 527 520 520 464 518 C380 512 330 498 290 470 C248 440 200 382 173 308 Z"/>
      </g></g>
    </svg>
  );
}
