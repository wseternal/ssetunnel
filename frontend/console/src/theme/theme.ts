import { createTheme } from '@mui/material/styles';

// Mercury Console design tokens (styles.md §2.1)
const INK = '#0f172a';
const SLATE = '#475569';
const SLATE_SOFT = '#64748b';
const SLATE_FAINT = '#94a3b8';
const HAIRLINE = '#e2e8f0';
const INPUT_BORDER = '#cbd5e1';
const CANVAS = '#f8fafc';
const ACCENT = '#4f46e5';
const ACCENT_HOVER = '#4338ca';
const ACCENT_SOFT = '#eef2ff';

export const OVERLAY_SHADOW = '0 12px 32px -8px rgba(15,23,42,0.12)';

const FONT_STACK = '"Inter Variable", "Inter", system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif';

export const theme = createTheme({
  palette: {
    mode: 'light',
    primary: { main: ACCENT, dark: ACCENT_HOVER, light: ACCENT_SOFT },
    secondary: { main: SLATE_SOFT },
    text: { primary: INK, secondary: SLATE, disabled: SLATE_FAINT },
    background: { default: CANVAS, paper: '#ffffff' },
    divider: HAIRLINE,
    action: {
      hover: 'rgba(15, 23, 42, 0.04)',
      selected: ACCENT_SOFT,
      disabled: SLATE_FAINT,
      disabledBackground: 'rgba(15, 23, 42, 0.06)',
    },
    success: { main: '#16a34a', dark: '#15803d' },
    error: { main: '#dc2626', dark: '#b91c1c' },
    warning: { main: '#d97706', dark: '#b45309' },
    info: { main: '#2563eb', dark: '#1d4ed8' },
  },
  typography: {
    fontFamily: FONT_STACK,
    h1: { fontWeight: 700, letterSpacing: '-0.02em' },
    h2: { fontWeight: 700, letterSpacing: '-0.015em' },
    h3: { fontWeight: 600, letterSpacing: '-0.01em' },
    h4: { fontWeight: 600 },
    h5: { fontWeight: 600 },
    h6: { fontWeight: 600 },
    body1: { fontSize: '0.875rem' },
    body2: { fontSize: '0.8125rem' },
    overline: {
      fontSize: '0.6875rem',
      fontWeight: 600,
      letterSpacing: '0.05em',
      textTransform: 'uppercase',
    },
    button: { textTransform: 'none', fontWeight: 600 },
  },
  shape: { borderRadius: 8 },
  components: {
    MuiPaper: {
      defaultProps: { elevation: 0 },
      styleOverrides: {
        root: ({ ownerState }: { ownerState: { elevation?: number } }) => ({
          ...(ownerState.elevation === 0 && { border: `1px solid ${HAIRLINE}` }),
        }),
      },
    },
    MuiTableCell: {
      styleOverrides: {
        root: { borderColor: HAIRLINE, fontSize: '0.8125rem' },
        head: {
          backgroundColor: CANVAS,
          color: SLATE,
          fontWeight: 600,
          fontSize: '0.6875rem',
          letterSpacing: '0.05em',
          textTransform: 'uppercase',
          lineHeight: 1.4,
        },
      },
    },
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: { root: { borderRadius: 6 } },
    },
    MuiOutlinedInput: {
      styleOverrides: {
        root: {
          backgroundColor: '#ffffff',
          fontSize: '0.8125rem',
          '& .MuiOutlinedInput-notchedOutline': { borderColor: INPUT_BORDER },
          '&:hover .MuiOutlinedInput-notchedOutline': { borderColor: SLATE_FAINT },
          '&.Mui-focused .MuiOutlinedInput-notchedOutline': {
            borderColor: ACCENT,
            boxShadow: '0 0 0 3px rgba(79, 70, 229, 0.12)',
          },
        },
      },
    },
    MuiInputLabel: {
      styleOverrides: { root: { fontSize: '0.8125rem' } },
    },
    MuiDialog: {
      styleOverrides: {
        paper: {
          borderRadius: 12,
          border: `1px solid ${HAIRLINE}`,
          boxShadow: OVERLAY_SHADOW,
        },
      },
    },
    MuiTabs: {
      styleOverrides: { indicator: { height: 2 } },
    },
    MuiTab: {
      styleOverrides: {
        root: {
          textTransform: 'none',
          fontSize: '0.8125rem',
          minHeight: 40,
          padding: '8px 16px',
          color: SLATE,
          '&.Mui-selected': { color: INK, fontWeight: 600 },
        },
      },
    },
    MuiSkeleton: {
      styleOverrides: { root: { backgroundColor: '#f1f5f9' } },
    },
    MuiTooltip: {
      styleOverrides: {
        tooltip: {
          backgroundColor: INK,
          fontSize: '0.6875rem',
          fontWeight: 500,
          borderRadius: 6,
        },
      },
    },
  },
});
