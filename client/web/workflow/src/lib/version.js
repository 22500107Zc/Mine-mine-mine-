/**
 * Version utility functions
 */

/**
 * Parses a version string into major and minor components
 * @param {string} version - Version string (e.g., "2024.1.0")
 * @returns {Object} Object with year and month properties
 */
export function parseVersion (version) {
  if (!version) return { year: '', month: '' }

  const [year, month] = version.split('.')
  return { year, month }
}

/**
 * Returns the help URL for the given documentation topic
 * @param {string} path - Documentation path (e.g., "integrator-guide/automation/workflows/index.html")
 * @returns {string} Full documentation URL
 */
// eslint-disable-next-line no-unused-vars
export function getDocumentationURL (path) {
  // CulpOS: product help is provided through the support page
  return '/support'
}
