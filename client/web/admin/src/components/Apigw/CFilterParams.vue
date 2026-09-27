<template>
  <div v-if="filter.params.length">
    <b-form-group
      v-for="(param, index) in filter.params"
      :key="index"
      class="text-primary"
    >
      <template slot="label">
        {{ $t(`filters.labels.${param.label}`) }}

        <template v-if="param.label === 'expr'">
          <a
            v-if="param.label === 'expr'"
            :href="documentationURL"
            target="_blank"
          >
            <font-awesome-icon
              :icon="['far', 'question-circle']"
            />
          </a>
        </template>
      </template>

      <!-- TODO create multi field component-->
      <b-form-checkbox
        v-if="param.type === 'bool'"
        v-model="param.value"
      />

      <c-input-select
        v-else-if="param.label === 'workflow'"
        v-model="param.value"
        :options="workflows"
        :get-option-key="getOptionKey"
        :reduce="wf => wf.workflowID"
        :placeholder="$t('filters.placeholders.workflow')"
        :loading="loadingWorkflows"
      />

      <b-form-select
        v-else-if="param.label === 'status'"
        v-model="param.value"
        :options="httpStatusOptions"
      >
        <template #first>
          <b-form-select-option
            :value="undefined"
          >
            {{ $t('filters.httpStatus.none') }}
          </b-form-select-option>
        </template>
      </b-form-select>

      <template v-else-if="filter.ref === 'response'">
        <template v-if="param.type === 'input'">
          <b-form-select
            v-model="param.value.type"
            :options="inputTypeOptions"
            class="mb-2"
          />

          <b-input-group>
            <b-input-group-prepend>
              <b-input-group-text variant="extra-light">
                ƒ
              </b-input-group-text>
            </b-input-group-prepend>
            <b-form-input
              v-model="param.value.expr"
              :placeholder="$t('filters.help.expression.example')"
            />
          </b-input-group>
        </template>

        <template v-else>
          <c-form-table-wrapper
            :labels="{ addButton: $t('filters.addHeader') }"
            @add-item="param.value.push({ name: '', expr: '' })"
          >
            <b-input-group
              v-for="(header, hIndex) in param.value"
              :key="`header-${hIndex}`"
              class="mb-2"
            >
              <b-form-input
                v-model="header.name"
                :placeholder="$t('filters.labels.name')"
              />
              <b-form-input
                v-model="header.expr"
                :placeholder="$t('filters.labels.value')"
              />

              <c-input-confirm
                show-icon
                @confirmed="param.value.splice(hIndex, 1)"
              />
            </b-input-group>
          </c-form-table-wrapper>
        </template>
      </template>

      <template v-else>
        <b-form-textarea
          v-if="param.label === 'jsfunc'"
          v-model="param.value"
          max-rows="6"
        />

        <b-input-group v-else>
          <b-input-group-prepend
            v-if="param.label === 'expr'"
          >
            <b-input-group-text variant="extra-light">
              ƒ
            </b-input-group-text>
          </b-input-group-prepend>

          <b-form-input
            v-if="param.label === 'expr'"
            v-model="param.value"
            :placeholder="$t('filters.help.expression.example')"
          />
          <b-form-input
            v-else
            v-model="param.value"
          />
        </b-input-group>
      </template>
    </b-form-group>
  </div>
</template>

<script>
export default {
  props: {
    filter: {
      type: Object,
      default: () => ({}),
    },
  },

  data () {
    return {
      loadingWorkflows: false,
      workflows: [],

      httpStatusOptions: [
        { value: 300, text: this.$t('filters.httpStatus.300') },
        { value: 301, text: this.$t('filters.httpStatus.301') },
        { value: 302, text: this.$t('filters.httpStatus.302') },
        { value: 303, text: this.$t('filters.httpStatus.303') },
        { value: 304, text: this.$t('filters.httpStatus.304') },
        { value: 307, text: this.$t('filters.httpStatus.307') },
        { value: 308, text: this.$t('filters.httpStatus.308') },
      ],

      inputTypeOptions: [
        'String',
        'Any',
        'Array',
        'KV',
        'DateTime',
        'Float',
        'Integer',
        'Reader',
        'Vars',
      ],
    }
  },

  computed: {
    documentationURL () {
      // eslint-disable-next-line no-undef
      return '/support'
    },
  },

  created () {
    if (this.filter.params.some(({ label = '' }) => label === 'workflow')) {
      this.loadingWorkflows = true

      this.$AutomationAPI.workflowList()
        .then(({ set: workflows = [] }) => {
          this.workflows = workflows.map(({ workflowID, handle, meta }) => {
            return { label: meta.name || handle, workflowID }
          })
        })
        .finally(() => {
          this.loadingWorkflows = false
        })
    }
  },

  methods: {
    getOptionKey ({ workflowID }) {
      return workflowID
    },
  },
}
</script>
