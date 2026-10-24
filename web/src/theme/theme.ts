import { createTheme, type CSSVariablesResolver, type MantineColorsTuple } from '@mantine/core'

/**
 * Deed design tokens (ADR-011, DISCOVERY Q-46): neutral grays, one indigo accent (#4263eb, the logo colour), restrained
 * 4 px radii, Inter for text and JetBrains Mono for identifiers.
 */
const deed: MantineColorsTuple = [
  '#edf2ff',
  '#dbe4ff',
  '#bac8ff',
  '#91a7ff',
  '#748ffc',
  '#5c7cfa',
  '#4c6ef5',
  '#4263eb',
  '#3b5bdb',
  '#364fc7',
]

export const theme = createTheme({
  primaryColor: 'deed',
  primaryShade: { light: 7, dark: 5 },
  colors: { deed },
  autoContrast: true,
  luminanceThreshold: 0.3,
  fontFamily: "'Inter Variable', Inter, system-ui, -apple-system, 'Segoe UI', sans-serif",
  fontFamilyMonospace: "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace",
  headings: {
    fontFamily: "'Inter Variable', Inter, system-ui, sans-serif",
    fontWeight: '600',
    sizes: {
      h1: { fontSize: '22px', lineHeight: '1.3' },
      h2: { fontSize: '18px', lineHeight: '1.35' },
      h3: { fontSize: '15px', lineHeight: '1.4' },
      h4: { fontSize: '13px', lineHeight: '1.45' },
    },
  },
  defaultRadius: 'sm',
  radius: { xs: '2px', sm: '4px', md: '4px', lg: '6px', xl: '8px' },
  fontSizes: { xs: '11px', sm: '12px', md: '13px', lg: '15px', xl: '18px' },
  spacing: { xs: '6px', sm: '8px', md: '12px', lg: '16px', xl: '24px' },
  cursorType: 'pointer',
  focusRing: 'auto',
  components: {
    Button: { defaultProps: { size: 'xs' } },
    ActionIcon: { defaultProps: { size: 'md', variant: 'subtle', color: 'gray' } },
    TextInput: { defaultProps: { size: 'xs' } },
    PasswordInput: { defaultProps: { size: 'xs' } },
    NumberInput: { defaultProps: { size: 'xs' } },
    Select: { defaultProps: { size: 'xs', comboboxProps: { withinPortal: true } } },
    MultiSelect: { defaultProps: { size: 'xs' } },
    Textarea: { defaultProps: { size: 'xs' } },
    TagsInput: { defaultProps: { size: 'xs' } },
    Switch: { defaultProps: { size: 'xs' } },
    Checkbox: { defaultProps: { size: 'xs' } },
    Radio: { defaultProps: { size: 'xs' } },
    Autocomplete: { defaultProps: { size: 'xs' } },
    SegmentedControl: { defaultProps: { size: 'xs' } },
    Badge: {
      defaultProps: { size: 'sm', radius: 'sm', variant: 'light' },
      styles: { label: { textTransform: 'none' } },
    },
    Table: { defaultProps: { fz: 'sm', verticalSpacing: 6, horizontalSpacing: 'sm', highlightOnHover: true } },
    Card: { defaultProps: { withBorder: true, padding: 'md', radius: 'sm' } },
    Paper: { defaultProps: { radius: 'sm' } },
    Tooltip: { defaultProps: { openDelay: 250, fz: 'xs', withArrow: true } },
    NavLink: { defaultProps: { py: 6 } },
    Tabs: { defaultProps: { keepMounted: false } },
    // Close buttons need an accessible name (axe button-name). Scroll lock (react-remove-scroll) injects a <style>
    // without the CSP nonce, so it stays off.
    Modal: { defaultProps: { closeButtonProps: { 'aria-label': 'Close' }, lockScroll: false } },
    Drawer: { defaultProps: { closeButtonProps: { 'aria-label': 'Close' }, lockScroll: false } },
    Notification: { defaultProps: { closeButtonProps: { 'aria-label': 'Close notification' } } },
    Popover: { defaultProps: { shadow: 'md' } },
  },
})

/**
 * Text colour of the light and outline variants (badges, alerts, buttons) in the light scheme. Mantine's shade 6 fails
 * WCAG AA on its own tint; these reach at least 4.5:1 over the tint and over white.
 */
const lightVariantText: Record<string, string> = {
  deed: '#2f44ad',
  blue: '#1864ab',
  cyan: '#0b7285',
  grape: '#862e9c',
  green: '#2b7a3b',
  indigo: '#364fc7',
  lime: '#4b7209',
  orange: '#b03b0b',
  pink: '#a61e4d',
  red: '#c02626',
  teal: '#08745a',
  violet: '#5f3dc4',
  yellow: '#8a4b00',
  gray: '#343a40',
}

/** Mantine's default dimmed grey (#868e96) and error red (#fa5252) fail WCAG AA on white. */
export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  light: {
    '--mantine-color-dimmed': 'var(--mantine-color-gray-7)',
    '--mantine-color-error': '#c02626',
    '--mantine-color-anchor': '#3b5bdb',
    ...Object.fromEntries(Object.entries(lightVariantText).map(([c, v]) => [`--mantine-color-${c}-light-color`, v])),
    ...Object.fromEntries(Object.entries(lightVariantText).map(([c, v]) => [`--mantine-color-${c}-outline`, v])),
  },
  dark: {
    '--mantine-color-dimmed': 'var(--mantine-color-dark-1)',
    '--mantine-color-anchor': '#91a7ff',
    '--mantine-color-deed-light-color': '#bac8ff',
    '--mantine-color-red-light-color': '#ffa8a8',
    '--mantine-color-yellow-light-color': '#ffe066',
    '--mantine-color-orange-light-color': '#ffc078',
    '--mantine-color-green-light-color': '#8ce99a',
    '--mantine-color-teal-light-color': '#96f2d7',
    '--mantine-color-grape-light-color': '#eebefa',
    '--mantine-color-gray-light-color': '#ced4da',
    '--mantine-color-blue-light-color': '#a5d8ff',
    '--mantine-color-cyan-light-color': '#99e9f2',
    '--mantine-color-violet-light-color': '#d0bfff',
    '--mantine-color-pink-light-color': '#fcc2d7',
    '--mantine-color-indigo-light-color': '#bac8ff',
  },
})
