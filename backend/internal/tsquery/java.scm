;; Java Tree-sitter queries for CodeGuard

;; Class declarations
(class_declaration
  name: (identifier) @class.name
  body: (class_body) @class.body)

;; Interface declarations
(interface_declaration
  name: (identifier) @interface.name
  body: (interface_body) @interface.body)

;; Method declarations
(method_declaration
  name: (identifier) @method.name
  parameters: (formal_parameters) @method.params
  body: (block) @method.body)

;; Constructor declarations
(constructor_declaration
  name: (identifier) @constructor.name
  parameters: (formal_parameters) @constructor.params
  body: (constructor_body) @constructor.body)

;; Field declarations
(field_declaration
  type: (_) @field.type
  (variable_declarator
    name: (identifier) @field.name))

;; Import declarations
(import_declaration
  (identifier) @import.path)

;; Method invocations
(method_invocation
  name: (identifier) @call.name
  arguments: (argument_list) @call.args)
