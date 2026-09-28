const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Workflow Configuration',
  appName: 'workflow',
  appLabel: 'St.Cloud~OS | Workflow Configuration',
  theme: 'corteza-base',
  packageAlias: 'corteza-workflow',
})
