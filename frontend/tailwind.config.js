/** @type {import('tailwindcss').Config} */
//
// Tailwind config for the precompiled stylesheet frontend/dist/libs/app.css.
// Build with scripts/build_frontend_css.sh (Tailwind standalone CLI v3.4.17).
//
// The markup is a single hand-written file whose class names (including the
// ones inside :class bindings) are all literal strings, so the content scan
// finds them. The safelist below covers class families that may only ever be
// assembled at runtime (status badges, state colours, spinners) so that adding
// a new status value in app.js cannot silently ship without its styles.
module.exports = {
  content: {
    relative: true,
    files: ['./dist/index.html', './dist/libs/app.js'],
  },
  safelist: [
    // Status / state colour families used by badges, dots, rows and toasts.
    { pattern: /^(bg|text|border)-(green|red|yellow|blue|gray|orange|purple|indigo|teal|slate)-(50|100|200|300|400|500|600|700|800|900)$/ },
    // Hover variants the sidebar / buttons switch between.
    { pattern: /^(bg|text)-(green|red|yellow|blue|gray|orange|purple|indigo)-(50|100|200|300|400|500|600|700|800|900)$/, variants: ['hover'] },
    // Spinners and transient states.
    'animate-spin',
    'animate-pulse',
    'opacity-50',
    'cursor-not-allowed',
    'border-transparent',
    'border-l-4',
    'border-b-2',
  ],
  theme: {
    extend: {},
  },
  plugins: [],
};
