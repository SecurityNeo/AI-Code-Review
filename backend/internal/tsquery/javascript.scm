;; JavaScript Tree-sitter queries for CodeGuard

;; Function declarations
(function_declaration
  name: (identifier) @function.name
  parameters: (formal_parameters) @function.params
  body: (statement_block) @function.body)

;; Arrow functions
(arrow_function
  parameters: (formal_parameters) @function.params
  body: (_) @function.body)

;; Class declarations
(class_declaration
  name: (identifier) @class.name
  super_class: (identifier)? @class.extends
  body: (class_body) @class.body)

;; Method definitions
(method_definition
  name: (property_identifier) @method.name
  parameters: (formal_parameters) @method.params
  body: (statement_block) @method.body)

;; Field definitions
(field_definition
  property: (property_identifier) @field.name)

;; Import statements
(import_statement
  source: (string) @import.source)

;; Call expressions
(call_expression
  function: (_) @call.function
  arguments: (arguments) @call.args)
