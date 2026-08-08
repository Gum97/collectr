/** Tokens read off the hi-fi mockups rather than invented.
 *
 *  The one rule the mockups state out loud, and the one worth keeping: forest
 *  green carries ordinary actions, earth red appears only where there is a legal
 *  consequence. A product whose red means both "delete this draft" and "you are
 *  past a statutory deadline" has taught its operators to ignore red.
 */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        canvas: '#eceee9',
        surface: '#ffffff',
        panel: '#fafbfa',
        chrome: '#eef0f2',

        ink: '#14171a',
        muted: '#5b6470',
        faint: '#8a929c',
        ghost: '#a8b0b8',

        line: '#dde1e5',
        'line-soft': '#e4e7ea',

        // Ordinary actions, and only those.
        accent: '#3d5a3c',
        'accent-dark': '#2c4230',
        'accent-line': '#cfdccd',
        'accent-wash': '#eef2ed',

        // Reserved for legal consequence: a missed statutory deadline, an
        // irreversible erasure, a hold that overrides retention. Never for a
        // failed form field.
        legal: '#a8432a',
        'legal-line': '#e0d3cd',
        'legal-wash': '#fbf3f0',

        // Compliance states stay distinct from each other. "Overdue" and "due
        // soon" are different obligations, not two shades of bad.
        overdue: '#a8432a',
        duesoon: '#8a6a1f',
        ok: '#3d5a3c',
      },
      fontFamily: {
        display: ['Spectral', 'Georgia', 'serif'],
        sans: ["'IBM Plex Sans'", 'system-ui', '-apple-system', 'sans-serif'],
        mono: ["'IBM Plex Mono'", 'ui-monospace', 'Menlo', 'monospace'],
      },
      fontSize: {
        // The mockups work in half-pixel steps between 11.5 and 14. Named here so
        // screens stop guessing.
        meta: ['11.5px', { lineHeight: '1.45' }],
        chip: ['12px', { lineHeight: '1.4' }],
        body: ['13px', { lineHeight: '1.55' }],
        lede: ['13.5px', { lineHeight: '1.6' }],
      },
      borderRadius: { DEFAULT: '5px', card: '7px', panel: '10px' },
      letterSpacing: { label: '.08em', caps: '.12em' },
    },
  },
  plugins: [],
}
