package saas

// CulpOS visual theme for the web applications (launcher, workspace,
// sign-in pages). Both color modes use the same palette so the product looks
// the same whichever mode a person picks.
const webappPalette = `{
	"black": "#E7ECF2",
	"white": "#12171D",
	"primary": "#00E0C0",
	"secondary": "#8E97A0",
	"success": "#3DDC84",
	"warning": "#FFB830",
	"danger": "#FF6B6B",
	"light": "#1A2027",
	"extra-light": "#262C33",
	"body-bg": "#0B0F13",
	"sidebar-bg": "#0B0F13",
	"topbar-bg": "#0B0F13"
}`

// webappCSS is compiled with the theme (SASS variables are allowed)
const webappCSS = `$font-light: 'Plex-Regular';
$font-regular: 'Plex-Regular';
$font-medium: 'Plex-Medium';
$font-semibold: 'Plex-Semibold';
$font-bold: 'Plex-Semibold';
$border-radius: 2px;
$border-radius-lg: 4px;
$border-radius-sm: 2px;

@font-face { font-family: 'Plex-Regular'; font-display: swap; src: url('/culpos/static/fonts/ibm-plex-sans-latin-400-normal.woff2') format('woff2'); }
@font-face { font-family: 'Plex-Medium'; font-display: swap; src: url('/culpos/static/fonts/ibm-plex-sans-latin-500-normal.woff2') format('woff2'); }
@font-face { font-family: 'Plex-Semibold'; font-display: swap; src: url('/culpos/static/fonts/ibm-plex-sans-latin-600-normal.woff2') format('woff2'); }
@font-face { font-family: 'Plex-Mono'; font-display: swap; src: url('/culpos/static/fonts/ibm-plex-mono-latin-400-normal.woff2') format('woff2'); }
@font-face { font-family: 'Plex-Mono-Medium'; font-display: swap; src: url('/culpos/static/fonts/ibm-plex-mono-latin-500-normal.woff2') format('woff2'); }

html, body { color-scheme: dark; }
body {
	background-color: #0B0F13 !important;
	background-image: linear-gradient(#161B20 1px, transparent 1px), linear-gradient(90deg, #161B20 1px, transparent 1px) !important;
	background-size: 56px 56px !important;
	background-attachment: fixed !important;
}
#app, .auth, main, .main, .content, .page, .bg-light { background-color: transparent !important; background-image: none !important; }

/* square-cornered panels, like the Command Deck */
.card, .rounded, .rounded-lg, .modal-content, .dropdown-menu, .vue-grid-item .card { border-radius: 4px !important; }
.vue-grid-item .card-body { overflow-y: auto; }

/* avatars */
.b-avatar, .avatar { background-color: #12171D !important; color: #00E0C0 !important; border: 1px solid #262C33 !important; }

/* panels */
.card { background-color: #12171D; border: 1px solid #262C33 !important; box-shadow: none !important; }
.card-header, .card-footer { background-color: transparent !important; border-color: #262C33 !important; }
.shadow, .shadow-sm, .shadow-lg { box-shadow: none !important; }
.border, .border-top, .border-bottom, .border-left, .border-right { border-color: #262C33 !important; }

/* signature mono captions */
.card-header h5, .card-header .h5, .card-title, h5.block-title, .block-title,
table thead th, .table thead th, .b-table thead th, label.text-primary, .col-form-label, legend {
	font-family: 'Plex-Mono-Medium', monospace !important;
	font-size: 12px !important;
	letter-spacing: .18em;
	text-transform: uppercase;
	color: #8E97A0 !important;
	font-weight: 500;
}
.card-header h5, .card-header .h5, .card-title { color: #E7ECF2 !important; font-size: 13px !important; letter-spacing: .22em; }
code, pre, kbd, samp, .text-monospace { font-family: 'Plex-Mono', monospace !important; }

/* buttons */
.btn { font-family: 'Plex-Mono-Medium', monospace !important; letter-spacing: .12em; text-transform: uppercase; font-size: 12px; }
.btn-primary { color: #04130F !important; }
.btn-light, .btn-extra-light, .btn-outline-light { background-color: transparent !important; border-color: #262C33 !important; color: #E7ECF2 !important; }
.btn-light:hover, .btn-extra-light:hover, .btn-outline-light:hover { border-color: #8E97A0 !important; }
.btn-outline-primary { color: #00E0C0 !important; border-color: #00E0C0 !important; }
.btn-outline-primary:hover { background-color: rgba(0, 224, 192, .1) !important; }

/* forms */
.form-control, .custom-select, .vs__dropdown-toggle, .input-group-text {
	background-color: #0F1318 !important; border-color: #262C33 !important; color: #E7ECF2 !important;
}
.form-control:focus, .custom-select:focus { border-color: #00E0C0 !important; box-shadow: 0 0 0 3px rgba(0, 224, 192, .12) !important; }
.form-control::placeholder { color: #5D6670 !important; }
.vs__dropdown-menu, .dropdown-menu { background-color: #12171D !important; border-color: #262C33 !important; }
.dropdown-item { color: #E7ECF2 !important; }
.dropdown-item:hover, .dropdown-item:focus, .vs__dropdown-option--highlight { background-color: #1A2027 !important; color: #00E0C0 !important; }

/* tables */
.table, .b-table { color: #E7ECF2; }
.table td, .table th { border-color: #1C2127 !important; }
.table-hover tbody tr:hover, .b-table tbody tr:hover { background-color: rgba(255, 255, 255, .025) !important; }

/* navigation */
.nav-tabs .nav-link.active, .nav-pills .nav-link.active { background-color: transparent !important; color: #00E0C0 !important; border-color: #00E0C0 !important; }
.sidebar, .b-sidebar, .b-sidebar > .b-sidebar-body { border-right: 1px solid #262C33; }
.topbar, .header-navigation { border-bottom: 1px solid #262C33; }
a { color: #00E0C0; }

/* modals & toasts */
.modal-content, .toast, .b-toast .toast, .popover { background-color: #12171D !important; border: 1px solid #262C33 !important; color: #E7ECF2 !important; }
.modal-header, .modal-footer { border-color: #262C33 !important; }
.close { color: #E7ECF2 !important; text-shadow: none !important; }

/* sign-in pages */
.auth label { font-family: 'Plex-Mono-Medium', monospace !important; font-size: 12px !important; letter-spacing: .16em; text-transform: uppercase; color: #8E97A0 !important; }
.footer, .footer a, .version, .version a { color: #8E97A0 !important; }
.footer a:hover { color: #E7ECF2 !important; }
.auth .card, .auth .tabs { background-color: #12171D !important; border: 1px solid #262C33 !important; }
.auth .login-title { font-family: 'Plex-Mono-Medium', monospace; font-size: 14px !important; letter-spacing: .22em; text-transform: uppercase; }
.auth .nav-item.active { border-bottom: 2px solid #00E0C0 !important; }
.auth .header, .auth .bg-light { background-color: transparent !important; }
`

// WebappTheme returns the settings values for ui.studio.themes and
// ui.studio.custom-css
func WebappTheme() (themes, customCSS []map[string]string) {
	themes = []map[string]string{
		{"id": "light", "values": webappPalette},
		{"id": "dark", "values": webappPalette},
	}
	customCSS = []map[string]string{
		{"id": "general", "values": webappCSS},
		{"id": "light", "values": ""},
		{"id": "dark", "values": ""},
	}
	return
}
