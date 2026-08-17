;; Go Tree-sitter queries for CodeGuard

;; Function declarations
(function_declaration
  name: (identifier) @function.name
  parameters: (parameter_list) @function.params
  body: (block) @function.body)

;; Method declarations
(method_declaration
  receiver: (parameter_list) @method.receiver
  name: (field_identifier) @method.name
  parameters: (parameter_list) @method.params
  body: (block) @method.body)

;; Type declarations
(type_declaration
  (type_spec
    name: (type_identifier) @type.name
    type: (_) @type.definition))

;; Struct types
(struct_type
  (field_declaration_list
    (field_declaration
      name: (field_identifier) @field.name
      type: (_) @field.type)))

;; Interface types
(interface_type
  (method_spec_list
    (method_spec
      name: (field_identifier) @interface.method.name)))

;; Import declarations
(import_declaration
  (import_spec_list
    (import_spec
      path: (interpreted_string_literal) @import.path)))

;; Call expressions
(call_expression
  function: (_) @call.function
  arguments: (argument_list) @call.args)
