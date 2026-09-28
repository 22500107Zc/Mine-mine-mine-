// CulpOS API location (the web applications are configured automatically
// by the CulpOS server; this file is only used for standalone development)
window.CortezaAPI = 'https://app.example.com/api';

// The auth URL can be autoconfigured by replacing /api with /auth in the API URL
// or by appending /auth to the end of the API URL
// When this is not possible and your configuration is more exotic you can set it
// explicitly:
// window.CortezaAuth = 'https://app.example.com/api/auth';

// Configure the web application URL when your web applications are not placed on the root.
// This is autoconfigured from the value of <base> tag href attribute in most cases.
// window.CortezaWebapp = 'https://app.example.com';

// Set to true to enable i18next-pseudo
// Used to test translation string in a development environment
// Even if set to true, it will be disabled in production
window.i18nPseudoModeEnabled = false