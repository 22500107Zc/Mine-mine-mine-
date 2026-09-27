<template>
  <div
    v-if="loaded"
  >
    <b-form-group
      :label="$t('sanitizers.label')"
      :description="$t('sanitizers.description')"
      label-class="d-flex align-items-center text-primary"
    >
      <template #label>
        {{ $t('sanitizers.label') }}

        <b-button
          variant="link"
          :href="`${documentationURL}#value-sanitizers`"
          target="_blank"
          class="p-0 ml-auto"
        >
          {{ $t('general:label.examples') }}
        </b-button>
      </template>

      <c-form-table-wrapper
        :labels="{ addButton: $t('general:label.add') }"
        @add-item="field.expressions.sanitizers.push('')"
      >
        <field-expressions
          v-model="field.expressions.sanitizers"
          :placeholder="$t('sanitizers.expression.placeholder')"
          @remove="onRemove('sanitizers', $event)"
        />
      </c-form-table-wrapper>
    </b-form-group>

    <hr>

    <b-form-group
      label-class="d-flex align-items-center text-primary"
      :description="$t('validators.description')"
      class="mt-3"
    >
      <template #label>
        {{ $t('validators.label') }}

        <b-button
          variant="link"
          :href="`${documentationURL}#value-validators`"
          target="_blank"
          class="p-0 ml-auto"
        >
          {{ $t('general:label.examples') }}
        </b-button>
      </template>

      <c-form-table-wrapper
        :labels="{ addButton: $t('general:label.add') }"
        @add-item="field.expressions.validators.push({ test: '', error: '' })"
      >
        <field-expressions
          v-model="field.expressions.validators"
          v-slot="{ value }"
          :no-prompt="isValidatorEmpty"
          @remove="onRemove('validators', $event)"
        >
          <b-form-input
            v-model="value.test"
            :placeholder="$t('validators.expression.placeholder')"
          />
          <b-input-group-prepend>
            <b-button
              v-b-tooltip.noninteractive.hover="{ title: $t('validators.error.tooltip'), boundary: 'body' }"
              variant="warning"
            >
              !
            </b-button>
          </b-input-group-prepend>
          <b-form-input
            v-model="value.error"
            :placeholder="$t('validators.error.placeholder')"
          />
          <b-input-group-append>
            <field-translator
              v-if="field"
              :field="field"
              :module="module"
              :highlight-key="`expression.validator.${value.validatorID}.error`"
              :disabled="isNew(value)"
            />
          </b-input-group-append>
        </field-expressions>
      </c-form-table-wrapper>

      <b-checkbox
        v-model="field.expressions.disableDefaultValidators"
        :disabled="!field.expressions.validators || field.expressions.validators.length === 0"
        :value="true"
        :unchecked-value="false"
        class="mt-3"
      >
        {{ $t('validators.disableBuiltIn') }}
      </b-checkbox>
    </b-form-group>

    <hr>

    <b-form-group
      label-class="d-flex align-items-center text-primary"
      class="mt-3"
    >
      <template #label>
        {{ $t('constraints.description') }}
        <c-hint
          :tooltip="$t('constraints.tooltip.performance')"
          icon-class="text-warning"
        />
      </template>

      <c-input-checkbox
        v-model="fieldConstraint.exists"
        switch
        :labels="{
          on: $t('general:label.yes'),
          off: $t('general:label.no'),
        }"
        @change="toggleFieldConstraint"
      />

      <b-row
        v-if="fieldConstraint.exists"
        class="mt-4"
      >
        <b-col
          cols="12"
          lg="6"
        >
          <b-form-group
            :label="$t('constraints.valueModifiers')"
            label-class="text-primary"
          >
            <b-form-select
              v-model="constraint.modifier"
              :options="modifierOptions"
            />
          </b-form-group>
        </b-col>
        <b-col
          cols="12"
          lg="6"
        >
          <b-form-group
            :label="$t('constraints.multiValues')"
            label-class="text-primary"
          >
            <b-form-select
              v-model="constraint.multiValue"
              :options="multiValueOptions"
              :disabled="!field.isMulti"
            />
          </b-form-group>
        </b-col>
        <b-col
          v-if="fieldConstraint.total"
          cols="12"
        >
          <i>
            {{ $t('constraints.totalFieldConstraintCount', { total: fieldConstraint.total }) }}
          </i>
        </b-col>
      </b-row>
    </b-form-group>
  </div>
</template>

<script>
import FieldExpressions from 'corteza-webapp-compose/src/components/Common/Module/FieldExpressions'
import FieldTranslator from 'corteza-webapp-compose/src/components/Admin/Module/FieldTranslator'
import { compose, NoID } from '@cortezaproject/corteza-js'

export default {
  i18nOptions: {
    namespaces: 'field',
  },

  components: {
    FieldExpressions,
    FieldTranslator,
  },

  props: {
    field: {
      type: compose.ModuleField,
      required: true,
    },

    module: {
      type: compose.Module,
      required: true,
    },
  },

  data () {
    return {
      loaded: false,
      fieldConstraint: {
        ruleIndex: null,
        total: 0,
        exists: false,
        index: null,
      },
      rule: {},
    }
  },

  computed: {
    documentationURL () {
      // eslint-disable-next-line no-undef
      return '/support'
    },

    modifierOptions () {
      return [
        { value: 'ignore-case', text: this.$t('constraints.ignoreCase') },
        { value: 'fuzzy-match', text: this.$t('constraints.fuzzyMatch') },
        { value: 'sounds-like', text: this.$t('constraints.soundsLike') },
        { value: 'case-sensitive', text: this.$t('constraints.caseSensitive') },
      ]
    },

    multiValueOptions () {
      return [
        { value: 'one-of', text: this.$t('constraints.oneOf') },
        { value: 'equal', text: this.$t('constraints.equal') },
      ]
    },

    constraint: {
      get () {
        if (this.module.config.recordDeDup.rules[this.fieldConstraint.ruleIndex]) {
          return this.module.config.recordDeDup.rules[this.fieldConstraint.ruleIndex].constraints[this.fieldConstraint.index]
        }

        return {}
      },

      set (value) {
        if (this.module.config.recordDeDup.rules[this.fieldConstraint.ruleIndex]) {
          this.module.config.recordDeDup.rules[this.fieldConstraint.ruleIndex].constraints[this.fieldConstraint.index] = value
        }
      },
    },
  },

  mounted () {
    this.checkForFieldConstraint()

    if (!this.field.expressions.sanitizers) {
      this.$set(this.field.expressions, 'sanitizers', [])
    }

    if (!this.field.expressions.validators) {
      this.$set(this.field.expressions, 'validators', [])
    }

    if (!this.field.expressions.disableDefaultValidators) {
      this.$set(this.field.expressions, 'disableDefaultValidators', false)
    }

    if (!this.field.expressions.formatters) {
      this.$set(this.field.expressions, 'formatters', [])
    }

    if (!this.field.expressions.disableDefaultFormatters) {
      this.$set(this.field.expressions, 'disableDefaultFormatters', false)
    }

    this.loaded = true
  },

  methods: {
    isNew (value) {
      return !(value.validatorID && value.validatorID !== NoID)
    },

    isValidatorEmpty ({ error = '', test = '' } = {}) {
      return error.length === 0 && test.length === 0
    },

    checkForFieldConstraint () {
      this.module.config.recordDeDup.rules.forEach((rule, x) => {
        const { constraints } = rule

        constraints.forEach((constraint, i) => {
          if (constraint.attribute === this.field.name) {
            if (constraints.length === 1) {
              this.fieldConstraint.exists = true
              this.fieldConstraint.index = i
              this.fieldConstraint.ruleIndex = x
            }

            this.fieldConstraint.total += 1
          }
        })
      })
    },

    toggleFieldConstraint (value) {
      if (!value) {
        this.module.config.recordDeDup.rules.splice(this.fieldConstraint.ruleIndex, 1)

        this.fieldConstraint.ruleIndex = null
        this.fieldConstraint.index = null
      } else if (this.fieldConstraint.ruleIndex == null) {
        this.module.config.recordDeDup.rules.push({
          name: '',
          strict: true,
          constraints: [{
            attribute: this.field.name,
            modifier: 'case-sensitive',
            multiValue: 'equal',
            type: this.field.kind,
          }],
        })

        this.fieldConstraint.ruleIndex = this.module.config.recordDeDup.rules.length - 1
        this.fieldConstraint.index = this.module.config.recordDeDup.rules[this.fieldConstraint.ruleIndex].constraints.length - 1
      }
    },

    onRemove (type, index) {
      this.field.expressions[type].splice(index, 1)
    },
  },
}
</script>
